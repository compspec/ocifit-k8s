package main

import (
	"bytes"
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
	"ghcr.io/compspec/ocifit-k8s/pkg/types"
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
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	enabledLabel             = "oci.image.compatibilities.selection/enabled"
	imageRefAnnotation       = "oci.image.compatibilities.selection/image-ref"
	targetImage              = "oci.image.compatibilities.selection/target-image"
	targetRefDefault         = "placeholder:latest"
	modelSelectionAnnotation = "oci.image.compatibilities.selection/model"
	modelServerEndpoint      = "http://localhost:5000/predict"

	instanceTypeLabel = "node.kubernetes.io/instance-type"
	instanceArchLabel = "kubernetes.io/arch"
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

// PredictionRequest is the payload sent to the Python model server.
type PredictionRequest struct {
	MetricName     string                   `json:"metric_name"`
	Directionality string                   `json:"directionality"`
	Features       []map[string]interface{} `json:"features"`
}

// PredictionResponse is the expected response from the Python model server.
type PredictionResponse struct {
	SelectedInstance map[string]interface{} `json:"selected_instance"`
	Instance	 string                 `json:"instance"`
	Arch             string                 `json:"arch"`
	Score            float64                `json:"score"`
	InstanceIndex    int                    `json:"instance_index"`
}


// WebhookServer with Node Cache and a direct k8s client
type WebhookServer struct {
	nodeLister   v1listers.NodeLister
	server       *http.Server
	k8sClient    client.Client
	stateLock    sync.RWMutex
	isHomogenous bool
	commonLabels map[string]string
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

// mutate is the core logic to look for compatibility labels and select a new image
func (ws *WebhookServer) mutate(ar *admissionv1.AdmissionReview) *admissionv1.AdmissionResponse {
	pod := &corev1.Pod{}
	if err := json.Unmarshal(ar.Request.Object.Raw, pod); err != nil {
		return deny(ar, fmt.Sprintf("could not decode pod object: %v", err))
	}

	if val, ok := pod.Labels[enabledLabel]; !ok || val != "true" {
		return admit(ar)
	}
	log.Printf("Mutating pod %s/%s", pod.Namespace, pod.Name)

	imageRef, ok := pod.Annotations[imageRefAnnotation]
	if !ok {
		return deny(ar, fmt.Sprintf("missing required annotation: %s", imageRefAnnotation))
	}
	targetRef, ok := pod.Annotations[targetImage]
	if !ok {
		targetRef = targetRefDefault
	}

	ctx := context.Background()
	genericSpec, err := artifact.DownloadCompatibilityArtifact(ctx, imageRef)
	if err != nil {
		return deny(ar, fmt.Sprintf("compatibility spec %s issue: %v", imageRef, err))
	}

	var finalImage, finalInstance string

	var patches []JSONPatch

	switch spec := genericSpec.(type) {
	case *types.ModelCompatibilitySpec:
		log.Println("Handling artifact as ModelCompatibilitySpec")
		finalInstance, finalImage, err = ws.selectImageWithModel(ctx, pod, spec)

		// For the model spec, we make a request to the server here
		if err != nil {
			return deny(ar, fmt.Sprintf("Issue with Model Compatibility Spec: %s", err))
		}

		log.Printf("Model selected instance type '%s'. Patching NodeSelector.", finalInstance)

		// If the pod's nodeSelector is nil, we need to create it with an "add" operation.
		if pod.Spec.NodeSelector == nil {
			patches = append(patches, JSONPatch{
				Op:   "add",
				Path: "/spec/nodeSelector",
				Value: map[string]string{
					instanceTypeLabel: finalInstance,
				},
			})

		} else {
			// If nodeSelector already exists, we add/replace the specific key.
			// JSON Patch requires escaping '/' with '~1' for keys in a path.
			escapedKey := strings.ReplaceAll(instanceTypeLabel, "/", "~1")
			patches = append(patches, JSONPatch{
				Op:    "add", // "add" on an existing map key works like "replace"
				Path:  fmt.Sprintf("/spec/nodeSelector/%s", escapedKey),
				Value: finalInstance,
			})
		}

	case *types.CompatibilitySpec:
		log.Println("Handling artifact as CompatibilitySpec")
		finalImage, err = ws.selectImageWithFeatures(pod, spec)
		if err != nil {
			return deny(ar, fmt.Sprintf("Issue with Compatibility Spec: %s", err))
		}

	default:
		return deny(ar, "downloaded artifact has an unknown or unsupported compatibility spec format")
	}

	// For the compatibility spec, we patch the container image (this is the selection)
	// For the model spec, the model provides a matching arch image.
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

	if err != nil {
		return deny(ar, fmt.Sprintf("failed to find compatible image: %v", err))
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

// selectImageWithFeatures uses the traditional NFD selector
func (ws *WebhookServer) selectImageWithFeatures(pod *corev1.Pod, spec *types.CompatibilitySpec) (string, error) {
	var nodeLabels map[string]string
	if len(pod.Spec.NodeSelector) > 0 {
		matchingNode, err := ws.findMatchingNode(pod.Spec.NodeSelector)
		if err != nil {
			return "", fmt.Errorf("failed to find a node: %w", err)
		}
		nodeLabels = matchingNode.Labels
	} else {
		ws.stateLock.RLock()
		isHomogenous, commonLabels := ws.isHomogenous, ws.commonLabels
		ws.stateLock.RUnlock()
		if !isHomogenous {
			return "", fmt.Errorf("pod has no nodeSelector and cluster is not homogenous. Please add a nodeSelector")
		}
		nodeLabels = commonLabels
	}
	return validator.EvaluateCompatibilitySpec(spec, nodeLabels)
}

// selectImageWithModel uses the model compatibility spec to ping the sidecar server and get the best instance type
// selectImageWithModel uses the model compatibility spec to ping the sidecar server and get the best instance type
func (ws *WebhookServer) selectImageWithModel(
	ctx context.Context,
	pod *corev1.Pod,
	spec *types.ModelCompatibilitySpec,
) (string, string, error) {
	modelHint, ok := pod.Annotations[modelSelectionAnnotation]
	if !ok {
		return "", "", fmt.Errorf("model-based spec was provided, but pod is missing annotation: %s", modelSelectionAnnotation)
	}

	var selectedRule *types.ModelSpec
	fmt.Println(spec.Compatibilities)
	for _, comp := range spec.Compatibilities {
		if comp.Tag == modelHint && len(comp.Rules) > 0 {
			selectedRule = &comp.Rules[0].MatchModel.Model
			break
		}
	}
	if selectedRule == nil {
		return "", "", fmt.Errorf("no model-based compatibility rule found for tag '%s'", modelHint)
	}

	nodeFeatures, err := ws.getAllNodeFeatures(ctx)
	if err != nil {
		return "", "", fmt.Errorf("failed to get node features: %w", err)
	}
	if len(nodeFeatures) == 0 {
		return "", "", fmt.Errorf("no schedulable nodes found in the cluster to evaluate")
	}

	// Make a request to the ML server to get the max/min for the given metric
	requestPayload := PredictionRequest{
		MetricName:     selectedRule.Name,
		Directionality: selectedRule.Direction,
		Features:       nodeFeatures,
	}
	log.Printf("Sending prediction request for model '%s' with %d nodes", requestPayload.MetricName, len(requestPayload.Features))
	prediction, err := callPredictEndpoint(ctx, requestPayload)
	fmt.Println(prediction)
	if err != nil {
		return "", "", fmt.Errorf("model prediction failed: %w", err)
	}
	log.Printf("Model server selected instance with features: %+v", prediction.SelectedInstance)

	// Get the right platform
	finalImage, ok := selectedRule.Platforms[prediction.Arch]
	if !ok {
		return "", "", fmt.Errorf("model does not provision architecture '%s'", prediction.Arch)
	}

	log.Printf("Model selected instance type: '%s'", prediction.Instance)
	return prediction.Instance, finalImage, nil
}

func callPredictEndpoint(ctx context.Context, payload PredictionRequest) (*PredictionResponse, error) {
	requestBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal prediction request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", modelServerEndpoint, bytes.NewBuffer(requestBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call model server at '%s': %w", modelServerEndpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("model server returned non-200 status: %s, body: %s", resp.Status, string(bodyBytes))
	}
	var prediction PredictionResponse
	if err := json.NewDecoder(resp.Body).Decode(&prediction); err != nil {
		return nil, fmt.Errorf("failed to decode prediction response: %w", err)
	}
	return &prediction, nil
}

func (ws *WebhookServer) getAllNodeFeatures(ctx context.Context) ([]map[string]interface{}, error) {
	var nodeList corev1.NodeList
	if err := ws.k8sClient.List(ctx, &nodeList, client.MatchingFields{"spec.unschedulable": "false"}); err != nil {
		return nil, err
	}
	var featureMatrix []map[string]interface{}
	for _, node := range nodeList.Items {
		features := make(map[string]interface{})
		for key, val := range node.Labels {
			features[key] = val
		}
		featureMatrix = append(featureMatrix, features)
	}
	return featureMatrix, nil
}

// getCompatibility labels returns all labels. We used to return just NFD but cannot
// know in advance which label might be useful
func getCompatibilityLabels(node *corev1.Node) map[string]string {
	labels := make(map[string]string)
	for key, val := range node.Labels {
		labels[key] = val
	}
	return labels
}

func (ws *WebhookServer) recalculateHomogeneity() {
	ws.stateLock.Lock()
	defer ws.stateLock.Unlock()

	log.Println("Recalculating cluster homogeneity...")

	workerNodeSelector, err := labels.Parse("!node-role.kubernetes.io/control-plane")
	if err != nil {
		log.Fatalf("FATAL: Failed to parse worker node selector: %v", err)
	}

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
	config, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		log.Fatalf("Error building kubeconfig: %s", err.Error())
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Error creating clientset: %s", err.Error())
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	ctrlClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		log.Fatalf("Failed to create controller-runtime client: %v", err)
	}

	stopCh := make(chan struct{})
	defer close(stopCh)

	ws := &WebhookServer{
		k8sClient: ctrlClient,
	}
	factory := informers.NewSharedInformerFactory(clientset, 10*time.Minute)
	nodeInformer := factory.Core().V1().Nodes().Informer()
	ws.nodeLister = factory.Core().V1().Nodes().Lister()

	nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			ws.recalculateHomogeneity()
		},
		DeleteFunc: func(obj interface{}) {
			ws.recalculateHomogeneity()
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldNode := oldObj.(*corev1.Node)
			newNode := newObj.(*corev1.Node)
			if !reflect.DeepEqual(getCompatibilityLabels(oldNode), getCompatibilityLabels(newNode)) {
				ws.recalculateHomogeneity()
			}
		},
	})

	go factory.Start(stopCh)
	if !cache.WaitForCacheSync(stopCh, nodeInformer.HasSynced) {
		log.Fatal("failed to wait for caches to sync")
	}

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

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutdown signal received, shutting down webhook server...")
	ws.server.Shutdown(context.Background())
}
