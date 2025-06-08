package artifact

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"ghcr.io/compspec/ocifit-k8s/pkg/types"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
)

// DownloadCompatibilityArtifact and return the parsed spec
func DownloadCompatibilityArtifact(ctx context.Context, specRef string) (*types.CompatibilitySpec, error) {
	log.Printf("Downloading compatibility spec from: %s", specRef)

	// --- Step 1: Connect to the registry and resolve the manifest by its tag ---
	reg, err := remote.NewRegistry(strings.Split(specRef, "/")[0])
	if err != nil {
		return nil, fmt.Errorf("failed to connect to registry for spec: %w", err)
	}
	repo, err := reg.Repository(ctx, strings.Trim(strings.SplitN(specRef, ":", 2)[0], "/"))
	if err != nil {
		return nil, fmt.Errorf("failed to access repository for spec: %w", err)
	}
	manifestDesc, err := repo.Resolve(ctx, strings.Split(specRef, ":")[1])
	if err != nil {
		return nil, fmt.Errorf("failed to resolve spec manifest %s: %w", specRef, err)
	}

	// --- Step 2: Fetch and parse the OCI Manifest itself ---
	log.Println("Fetching OCI manifest content...")
	manifestBytes, err := content.FetchAll(ctx, repo, manifestDesc)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch manifest content: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("failed to unmarshal OCI manifest: %w", err)
	}
	log.Printf("Successfully parsed OCI manifest (artifact type: %s)", manifest.ArtifactType)

	// --- Step 3: Find the correct layer within the manifest ---
	log.Printf("Searching for spec layer with media type: %s", types.CompatibilitySpecMediaType)
	var specLayerDesc *ocispec.Descriptor
	for _, layer := range manifest.Layers {
		if layer.MediaType == types.CompatibilitySpecMediaType {
			// Found it! Keep a pointer to this descriptor.
			specLayerDesc = &layer
			break
		}
	}

	if specLayerDesc == nil {
		return nil, fmt.Errorf("manifest does not contain a layer with media type %s", types.CompatibilitySpecMediaType)
	}
	log.Printf("Found spec layer with digest: %s", specLayerDesc.Digest)

	// --- Step 4: Fetch the content of the spec layer using its descriptor ---
	log.Println("Fetching compatibility spec content...")
	specBytes, err := content.FetchAll(ctx, repo, *specLayerDesc)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch spec layer content: %w", err)
	}

	// --- Step 5: Unmarshal the final spec JSON into our struct ---
	var spec types.CompatibilitySpec
	err = json.Unmarshal(specBytes, &spec)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal compatibility spec JSON: %w", err)
	}

	log.Printf("Successfully downloaded and parsed spec version %s", spec.Version)
	return &spec, nil

}
