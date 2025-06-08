package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"ghcr.io/compspec/ocifit-k8s/pkg/artifact"
	"ghcr.io/compspec/ocifit-k8s/pkg/validator"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	v1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// Label to turn compatibility selection on and off
	enabledLabel       = "oci.image.compatibilities.selection/enabled"
	imageRefAnnotation = "oci.image.compatibilities.selection/image-ref"
	targetImage        = "oci.image.compatibilities.selection/target-image"
	targetRefDefault   = "placeholder:latest"
)

var (
	universalDeserializer = serializer.NewCodecFactory(runtime.NewScheme()).UniversalDeserializer()
)

// JSONPatch represents a single JSON patch operation
type JSONPatch struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

// admit allows the request without modification
func admit(ar *admissionv1.AdmissionReview) *admissionv1.AdmissionResponse {
	if ar.Request.Name != "" {
		log.Printf("Allowing pod %s/%s: %s", ar.Request.Namespace, ar.Request.Name)
	} else {
		log.Printf("Allowing pod in namespace %s without mutation", ar.Request.Namespace)
	}
	return &admissionv1.AdmissionResponse{
		Allowed: true,
		UID:     ar.Request.UID,
	}
}

// deny rejects the request with a message
func deny(ar *admissionv1.AdmissionReview, message string) *admissionv1.AdmissionResponse {

	// Most pods don't have a name yet
	if ar.Request.Name != "" {
		log.Printf("Denying pod %s/%s: %s", ar.Request.Namespace, ar.Request.Name, message)
	} else {
		log.Printf("Denying pod in namespace %s: %s", ar.Request.Namespace, message)
	}
	return &admissionv1.AdmissionResponse{
		Allowed: false,
		UID:     ar.Request.UID,
		Result: &metav1.Status{
			Message: message,
		},
	}
}

// Determine if label is for NFD
// We use this to assess uniqueness (homogeneity of cluster)
func isCompatibilityLabel(key string) bool {
	// Note that the full URI is feature.node.kubernetes.io/
	// I'm truncating to feature.node so the features aren't Kubernetes specific
	return strings.HasPrefix(key, "feature.node") ||
		key == "kubernetes.io/arch" ||
		key == "kubernetes.io/os"
}

// Extracts only the compatibility-relevant labels from a node.
func getCompatibilityLabels(node *corev1.Node) map[string]string {
	labels := make(map[string]string)
	for key, val := range node.Labels {
		if isCompatibilityLabel(key) {
			labels[key] = val
		}
	}
	return labels
}

// recalculateHomogeneity performs the check and updates the cached state.
// This is the single source of truth for the homogeneity state.
func (ws *WebhookServer) recalculateHomogeneity() {
	// Acquire a full write lock to change the state.
	ws.stateLock.Lock()
	defer ws.stateLock.Unlock()

	log.Println("Recalculating cluster homogeneity...")

	// We can't include control plane nodes - they don't have NFD labels
	workerNodeSelector, err := labels.Parse("!node-role.kubernetes.io/control-plane")
	if err != nil {
		log.Fatalf("FATAL: Failed to parse worker node selector: %v", err)
	}

	// List only the nodes that match our selector (i.e., only worker nodes).
	nodes, err := ws.nodeLister.List(workerNodeSelector)
	if err != nil {
		log.Printf("Error listing nodes during homogeneity check: %v. Assuming NOT homogenous.", err)
		ws.isHomogenous = false
		ws.commonLabels = nil
		return
	}

	if len(nodes) < 2 {
		log.Println("Cluster has 0 or 1 nodes. Considered homogenous by default.")
		ws.isHomogenous = true
		if len(nodes) == 1 {
			ws.commonLabels = getCompatibilityLabels(nodes[0])
		} else {
			ws.commonLabels = nil
		}
		return
	}

	referenceLabels := getCompatibilityLabels(nodes[0])
	for i := 1; i < len(nodes); i++ {
		currentNodeLabels := getCompatibilityLabels(nodes[i])
		if !reflect.DeepEqual(referenceLabels, currentNodeLabels) {
			log.Printf("Homogeneity check failed: Node %s has different compatibility labels than %s.", nodes[i].Name, nodes[0].Name)
			ws.isHomogenous = false
			ws.commonLabels = nil
			return
		}
	}

	log.Printf("Homogeneity check passed. Caching %d common labels.", len(referenceLabels))
	ws.isHomogenous = true
	ws.commonLabels = referenceLabels
}

// WebhookServer with Node Cache
type WebhookServer struct {
	nodeLister v1listers.NodeLister
	server     *http.Server

	// Cached state and a lock to protect it
	stateLock    sync.RWMutex
	isHomogenous bool
	commonLabels map[string]string
}

// findMatchingNode searches the cache for a node that satisfies the pod's nodeSelector.
func (ws *WebhookServer) findMatchingNode(nodeSelector map[string]string) (*corev1.Node, error) {
	if len(nodeSelector) == 0 {
		return nil, fmt.Errorf("pod has no nodeSelector, cannot determine target node features")
	}

	selector := labels.SelectorFromSet(nodeSelector)
	nodes, err := ws.nodeLister.List(selector)
	if err != nil {
		return nil, fmt.Errorf("error listing nodes from cache: %w", err)
	}

	if len(nodes) == 0 {
		return nil, fmt.Errorf("no nodes found matching selector: %s", selector.String())
	}

	nodeObj := nodes[0]
	log.Printf("Found matching node for selector %s: %s", selector.String(), nodeObj.Name)
	return nodeObj, nil
}

// mutate is the core logic to look for compatibility labels and select a new image
func (ws *WebhookServer) mutate(ar *admissionv1.AdmissionReview) *admissionv1.AdmissionResponse {

	// Decode the Pod from the AdmissionReview
	pod := &corev1.Pod{}
	if err := json.Unmarshal(ar.Request.Object.Raw, pod); err != nil {
		return deny(ar, fmt.Sprintf("could not decode pod object: %v", err))
	}

	// Check for the trigger label. If not present, admit without changes.
	if val, ok := pod.Labels[enabledLabel]; !ok || val != "true" {
		return admit(ar)
	}
	log.Printf("Mutating pod %s/%s", pod.Namespace, pod.Name)

	// Get required annotations for the compatibility artifact URI lookup
	// This was pushed via an ORAS artifact
	imageRef, ok := pod.Annotations[imageRefAnnotation]
	if !ok {
		return deny(ar, fmt.Sprintf("missing required annotation: %s", imageRefAnnotation))
	}

	// Target image to replace in pod
	targetRef, ok := pod.Annotations[targetImage]
	if !ok {
		targetRef = targetRefDefault
	}

	// Determine the target node's labels. We either have a homogenous cluster
	// (all nodes are the same) or we have to use a node selector for the image.
	var nodeLabels map[string]string
	if len(pod.Spec.NodeSelector) > 0 {
		matchingNode, err := ws.findMatchingNode(pod.Spec.NodeSelector)
		if err != nil {
			return deny(ar, fmt.Sprintf("failed to find a node: %v", err))
		}
		nodeLabels = matchingNode.Labels
	} else {
		ws.stateLock.RLock()
		isHomogenous, commonLabels := ws.isHomogenous, ws.commonLabels
		ws.stateLock.RUnlock()
		if !isHomogenous {
			return deny(ar, "pod has no nodeSelector and cluster is not homogenous. Please add a nodeSelector.")
		}
		nodeLabels = commonLabels
	}

	// Download and parse the compatibility spec from the OCI registry.
	ctx := context.Background()

	// Download the artifact (compatibility spec) from the uri
	// TODO (vsoch) we should have mode to cache these and not need to re-download
	spec, err := artifact.DownloadCompatibilityArtifact(ctx, imageRef)
	if err != nil {
		return deny(ar, fmt.Sprintf("compatibility spec %s issue: %v", imageRef, err))
	}

	// Evaluate the spec against the node's labels to find the winning tag.
	// The "tag" attribute we are hijacking here to put the full container URI
	finalImage, err := validator.EvaluateCompatibilitySpec(spec, nodeLabels)
	if err != nil {
		return deny(ar, fmt.Sprintf("failed to find compatible image: %v", err))
	}

	// Create and apply the JSON patch
	var patches []JSONPatch
	containerFound := false
	for i, c := range pod.Spec.Containers {
		if c.Image == targetRef {
			patches = append(patches, JSONPatch{
				Op:    "replace",
				Path:  fmt.Sprintf("/spec/containers/%d/image", i),
				Value: finalImage,
			})
			containerFound = true
			break
		}
	}
	if !containerFound {
		return deny(ar, fmt.Sprintf("container %s not found", targetRef))
	}

	patchBytes, err := json.Marshal(patches)
	if err != nil {
		return deny(ar, fmt.Sprintf("failed to marshal patch: %v", err))
	}

	patchType := admissionv1.PatchTypeJSONPatch
	return &admissionv1.AdmissionResponse{
		Allowed:   true,
		UID:       ar.Request.UID,
		Patch:     patchBytes,
		PatchType: &patchType,
	}
}

// handleMutate is the HTTP handler for the compatibility webhook
func (ws *WebhookServer) handleMutate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "could not read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	ar := &admissionv1.AdmissionReview{}
	if _, _, err := universalDeserializer.Decode(body, nil, ar); err != nil {
		http.Error(w, fmt.Sprintf("could not decode admission review: %v", err), http.StatusBadRequest)
		return
	}

	response := ws.mutate(ar)
	respAR := admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{Kind: "AdmissionReview", APIVersion: "admission.k8s.io/v1"},
		Response: response,
	}

	respBody, err := json.Marshal(respAR)
	if err != nil {
		http.Error(w, fmt.Sprintf("could not encode response: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(respBody)
}

func main() {

	// Kubernetes Client and Informer Setup
	// We want to have a view of cluster nodes via NFD
	config, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		log.Fatalf("Error building kubeconfig: %s", err.Error())
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error creating clientset: %s", err.Error())
	}

	// Create a shared informer factory and a stop channel
	stopCh := make(chan struct{})
	defer close(stopCh)

	ws := &WebhookServer{}
	factory := informers.NewSharedInformerFactory(clientset, 10*time.Minute)
	nodeInformer := factory.Core().V1().Nodes().Informer()
	ws.nodeLister = factory.Core().V1().Nodes().Lister() // Assign lister to our struct

	// The informer has event handles to deal with nodes being added/removed
	// from the cluster.
	nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			ws.recalculateHomogeneity()
		},
		DeleteFunc: func(obj interface{}) {
			ws.recalculateHomogeneity()
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			// Only recalculate if compatibility labels have changed.
			oldNode := oldObj.(*corev1.Node)
			newNode := newObj.(*corev1.Node)
			if !reflect.DeepEqual(getCompatibilityLabels(oldNode), getCompatibilityLabels(newNode)) {
				ws.recalculateHomogeneity()
			}
		},
	})

	// Start informer and wait for cache sync
	go factory.Start(stopCh)
	if !cache.WaitForCacheSync(stopCh, nodeInformer.HasSynced) {
		log.Fatal("failed to wait for caches to sync")
	}

	// Perform the initial calculation after cache sync
	log.Println("Performing initial cluster homogeneity check...")
	ws.recalculateHomogeneity()

	certPath := os.Getenv("TLS_CERT_PATH")
	keyPath := os.Getenv("TLS_KEY_PATH")
	if certPath == "" || keyPath == "" {
		log.Fatal("TLS_CERT_PATH and TLS_KEY_PATH environment variables must be set")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/mutate", ws.handleMutate)

	ws.server = &http.Server{
		Addr:      ":8443",
		Handler:   mux,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}

	log.Println("Starting OCI compatibility image selector webhook server on :8443...")
	go func() {
		if err := ws.server.ListenAndServeTLS(certPath, keyPath); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Graceful (or not so graceful) shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutdown signal received, shutting down webhook server...")
	ws.server.Shutdown(context.Background())
}
