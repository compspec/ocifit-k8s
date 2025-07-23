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
	"ghcr.io/compspec/ocifit-k8s/pkg/flux"
	"ghcr.io/compspec/ocifit-k8s/pkg/types"
	"ghcr.io/compspec/ocifit-k8s/pkg/validator"

	miniclusterv1alpha2 "github.com/flux-framework/flux-operator/api/v1alpha2"
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
	skipInstanceTypes     = make(map[string]bool)
)

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

// mutate responds to the object kind, with different behavior depending on the type
func (ws *WebhookServer) mutate(ar *admissionv1.AdmissionReview) *admissionv1.AdmissionResponse {
	// Use the Kind from the AdmissionReview request to route to the correct handler
	switch ar.Request.Kind.Kind {
	case "Pod":
		return ws.mutatePod(ar)
	case "MiniCluster":
		return ws.mutateMiniCluster(ar)
	default:
		log.Printf("Webhook received unhandled kind '%s', allowing.", ar.Request.Kind.Kind)
		return admit(ar)
	}
}

// mutate is the core logic to look for compatibility labels and select a new image
func (ws *WebhookServer) mutatePod(ar *admissionv1.AdmissionReview) *admissionv1.AdmissionResponse {
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

	ctx := context.Background()
	genericSpec, err := artifact.DownloadCompatibilityArtifact(ctx, imageRef)
	if err != nil {
		return deny(ar, fmt.Sprintf("compatibility spec %s issue: %v", imageRef, err))
	}

	var finalImage, finalInstance string
	var prediction *types.PredictionResponse
	var patches []types.JSONPatch

	switch spec := genericSpec.(type) {
	case *types.ModelCompatibilitySpec:
		log.Println("Handling artifact as ModelCompatibilitySpec")
		finalImage, prediction, err = ws.selectImageWithModel(ctx, pod.Annotations, spec)

		// For the model spec, we make a request to the server here
		if err != nil {
			return deny(ar, fmt.Sprintf("Issue with Model Compatibility Spec: %s", err))
		}

		// If the pod's nodeSelector is nil, we need to create it with an "add" operation.
		if pod.Spec.NodeSelector == nil {
			patches = append(patches, types.JSONPatch{
				Op:   "add",
				Path: "/spec/nodeSelector",
				Value: map[string]string{
					prediction.InstanceSelector: prediction.Instance,
				},
			})

		} else {
			// If nodeSelector already exists, we add/replace the specific key.
			// JSON Patch requires escaping '/' with '~1' for keys in a path.
			escapedKey := strings.ReplaceAll(instanceTypeLabel, "/", "~1")
			patches = append(patches, types.JSONPatch{
				Op:    "add",
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

	patches, err = ws.replaceContainers(patches, pod.Annotations, pod.Spec.Containers, finalImage)
	if err != nil {
		return deny(ar, fmt.Sprintf("%s", err))
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

// replaceContainers updates containers via add patches
func (ws *WebhookServer) replaceContainers(
	patches []types.JSONPatch,
	annotations map[string]string,
	containers []corev1.Container,
	finalImage string,
) ([]types.JSONPatch, error) {

	targetRef, ok := annotations[targetImage]
	if !ok {
		targetRef = targetRefDefault
	}

	// Replace any placeholder images with the final. We need at least one.
	// Unlike the pod replacement, don't break - could be another container
	containerFound := false
	for i, c := range containers {
		if c.Image == targetRef {
			patches = append(patches, types.JSONPatch{
				Op:    "replace",
				Path:  fmt.Sprintf("/spec/containers/%d/image", i),
				Value: finalImage,
			})
			containerFound = true
		}
	}
	if !containerFound {
		return patches, fmt.Errorf("container %s not found", targetRef)
	}
	return patches, nil
}

// mutateMiniCluster still selects instance type and pods, but also exposes customization of the entire setup
func (ws *WebhookServer) mutateMiniCluster(ar *admissionv1.AdmissionReview) *admissionv1.AdmissionResponse {
	mc := &miniclusterv1alpha2.MiniCluster{}
	var rawObject map[string]interface{}
	err := json.Unmarshal(ar.Request.Object.Raw, mc)
	if err != nil {
		return deny(ar, fmt.Sprintf("could not decode MiniCluster object: %v", err))
	}

	// Let's use the raw object for checking additional fields
	err = json.Unmarshal(ar.Request.Object.Raw, &rawObject)
	if err != nil {
		return deny(ar, fmt.Sprintf("could not decode MiniCluster into raw object: %v", err))
	}

	// We use the MiniCluster's own labels and annotations
	if val, ok := mc.Labels[enabledLabel]; !ok || val != "true" {
		return admit(ar)
	}
	log.Printf("Mutating MiniCluster %s/%s", mc.Namespace, mc.Name)
	imageRef, ok := mc.Annotations[imageRefAnnotation]
	if !ok {
		return deny(ar, fmt.Sprintf("missing required annotation on MiniCluster: %s", imageRefAnnotation))
	}

	ctx := context.Background()
	genericSpec, err := artifact.DownloadCompatibilityArtifact(ctx, imageRef)
	if err != nil {
		return deny(ar, fmt.Sprintf("compatibility spec %s issue: %v", imageRef, err))
	}

	// For now, I'm assuming MiniClusters will primarily use Model specs.
	// You could expand this to use feature-based selection as well.
	spec, ok := genericSpec.(*types.ModelCompatibilitySpec)
	if !ok {
		return deny(ar, "received artifact for MiniCluster that was not a ModelCompatibilitySpec")
	}

	// For a MiniCluster, we assume we should evaluate against ALL nodes in the cluster
	// as the scheduler will decide where to place the pods later.
	// We pass nil for the nodeSelector to indicate this.
	finalImage, prediction, err := ws.selectImageWithModel(ctx, mc.Annotations, spec)
	if err != nil {
		return deny(ar, fmt.Sprintf("Issue with Model Compatibility Spec: %s", err))
	}

	// Replace all referenced containers based on target name
	containers := []corev1.Container{}
	for _, container := range mc.Spec.Containers {
		containers = append(containers, corev1.Container{Image: container.Image})
	}

	patches := flux.GetPatches(rawObject, prediction)
	patches, err = ws.replaceContainers(patches, mc.Annotations, containers, finalImage)
	if err != nil {
		return deny(ar, fmt.Sprintf("%s", err))
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

// selectImageWithModel uses the model compatibility spec to ping the sidecar server, get the best instance,
// and then look up the correct container image for the instance's architecture.
// We can accept annotations from a pod or MiniCluster (or other future abstraction)
func (ws *WebhookServer) selectImageWithModel(
	ctx context.Context,
	annotations map[string]string,
	spec *types.ModelCompatibilitySpec,
) (string, *types.PredictionResponse, error) {

	modelHint, ok := annotations[modelSelectionAnnotation]
	if !ok {
		return "", nil, fmt.Errorf("model-based spec was provided, but object is missing annotation: %s", modelSelectionAnnotation)
	}

	var selectedRule *types.ModelSpec
	for _, comp := range spec.Compatibilities {
		if comp.Tag == modelHint && len(comp.Rules) > 0 {
			selectedRule = &comp.Rules[0].MatchModel.Model
			break
		}
	}
	if selectedRule == nil {
		return "", nil, fmt.Errorf("no model-based compatibility rule found for tag '%s'", modelHint)
	}

	// The environment variable name here was different from your previous request,
	// using the name from this full script: "FEATURES_CACHE_DIR"
	nodeFeatures, err := ws.getCombinedNodeFeatures(ctx, os.Getenv("FEATURES_CACHE_DIR"))
	if err != nil {
		return "", nil, fmt.Errorf("failed to get combined node features: %w", err)
	}

	if len(nodeFeatures) == 0 {
		return "", nil, fmt.Errorf("no nodes found to evaluate for model selection")
	}

	requestPayload := types.PredictionRequest{
		MetricName:     selectedRule.Name,
		Directionality: selectedRule.Direction,
		Features:       nodeFeatures,
	}

	log.Printf("Sending prediction request for model '%s' with %d nodes", requestPayload.MetricName, len(requestPayload.Features))
	prediction, err := callPredictEndpoint(ctx, requestPayload)
	if err != nil {
		return "", nil, fmt.Errorf("model prediction failed: %w", err)
	}
	log.Printf("Model server selected instance with features: %+v", prediction.SelectedInstance)

	finalImage, ok := selectedRule.Platforms[prediction.Arch]
	if !ok {
		return "", nil, fmt.Errorf("model spec's 'platforms' map does not have an entry for the chosen architecture '%s'", prediction.Arch)
	}
	log.Printf("Model selected instance '%s' and arch '%s', resulting in final image: '%s'", prediction.Instance, prediction.Arch, finalImage)

	// Return the instance name for the nodeSelector patch and the final image for the container patch.
	return finalImage, prediction, nil
}

func callPredictEndpoint(ctx context.Context, payload types.PredictionRequest) (*types.PredictionResponse, error) {
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
	var prediction types.PredictionResponse
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

	// We will do a filtering here for types we don't want to include
	identityLabelKey := os.Getenv("NODE_IDENTITY_LABEL")
	if identityLabelKey == "" {
		identityLabelKey = instanceTypeLabel
	}

	var featureMatrix []map[string]interface{}
	for _, node := range nodeList.Items {
		features := make(map[string]interface{})
		for key, val := range node.Labels {
			features[key] = val
		}

		// Be conservative - don't include nodes we cannot identify
		identityValue, ok := features[identityLabelKey].(string)
		if !ok {
			continue
		}
		if skipInstanceTypes[identityValue] {
			log.Printf("Skipping dynamically discovered instance '%s' as it is in the skip list.", identityValue)
			continue
		}
		featureMatrix = append(featureMatrix, features)
	}
	return featureMatrix, nil
}

// getCombinedNodeFeatures merges dynamically discovered nodes with a static catalog from a directory.
// It correctly handles static files that contain a JSON array (list) of node feature objects.
func (ws *WebhookServer) getCombinedNodeFeatures(ctx context.Context, staticFeaturesDir string) ([]map[string]interface{}, error) {

	// Get all currently running schedulable nodes.
	dynamicallyDiscoveredFeatures, err := ws.getAllNodeFeatures(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not get dynamic node features: %w", err)
	}

	// Cut out early if no cache to load
	if staticFeaturesDir == "" {
		log.Println("STATIC_FEATURES_DIR not set, skipping static feature loading.")
		return dynamicallyDiscoveredFeatures, nil
	}

	// Determine which label to use as the unique node identifier (kind doesn't have instance type)
	identityLabelKey := os.Getenv("NODE_IDENTITY_LABEL")
	if identityLabelKey == "" {
		identityLabelKey = instanceTypeLabel // Fallback to the default
	}
	log.Printf("Using '%s' as the node identity label for de-duplication.", identityLabelKey)

	// Create the final list, starting with the dynamic nodes.
	combinedFeatures := make([]map[string]interface{}, 0)
	combinedFeatures = append(combinedFeatures, dynamicallyDiscoveredFeatures...)

	// Create a lookup map to track instance types we've already seen.
	// This now correctly uses the configurable identityLabelKey.
	seenInstanceTypes := make(map[string]bool)
	for _, features := range dynamicallyDiscoveredFeatures {
		if identityValue, ok := features[identityLabelKey].(string); ok {
			seenInstanceTypes[identityValue] = true
		}
	}
	if len(seenInstanceTypes) > 0 {
		log.Printf("Found %d dynamically discovered nodes. Instance identities seen: %v", len(dynamicallyDiscoveredFeatures), seenInstanceTypes)
	} else {
		log.Printf("Found %d dynamically discovered nodes.", len(dynamicallyDiscoveredFeatures))
	}

	files, err := os.ReadDir(staticFeaturesDir)
	if err != nil {
		log.Printf("Warning: Could not read static features directory '%s': %v. Proceeding with dynamic nodes only.", staticFeaturesDir, err)
		return combinedFeatures, nil
	}

	// Loop through the static files, parse them, and add them if they are new.
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}

		filePath := fmt.Sprintf("%s/%s", staticFeaturesDir, file.Name())
		content, err := os.ReadFile(filePath)
		if err != nil {
			log.Printf("Warning: Failed to read static feature file %s: %v", filePath, err)
			continue
		}
		var featureList []map[string]interface{}
		if err := json.Unmarshal(content, &featureList); err != nil {
			log.Printf("Warning: Failed to parse static feature file %s as a list of features: %v", filePath, err)
			continue
		}

		for _, staticFeatures := range featureList {
			identityValue, ok := staticFeatures[identityLabelKey].(string)
			if !ok {
				continue
			}
			if skipInstanceTypes[identityValue] {
				log.Printf("Skipping static instance '%s' from file '%s' as it is in the skip list.", identityValue, file.Name())
				continue
			}

			// De-duplication check...
			if identityValue, ok := staticFeatures[identityLabelKey].(string); ok {
				if !seenInstanceTypes[identityValue] {
					log.Printf("Adding new instance identity '%s' from static catalog file '%s'.", identityValue, file.Name())
					combinedFeatures = append(combinedFeatures, staticFeatures)
					seenInstanceTypes[identityValue] = true // Add to map to prevent duplicates
				}
			}
		}
	}

	return combinedFeatures, nil
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

func discoverSkipInstances() {
	skipList := os.Getenv("SKIP_INSTANCE_TYPES")
	if skipList != "" {
		instances := strings.Split(skipList, ",")
		for _, instance := range instances {
			trimmed := strings.TrimSpace(instance)
			if trimmed != "" {
				log.Printf("Configuration: Will skip instance type '%s'", trimmed)
				skipInstanceTypes[trimmed] = true
			}
		}
	}
}

func main() {

	// Ensure we parse skip instances
	discoverSkipInstances()

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
