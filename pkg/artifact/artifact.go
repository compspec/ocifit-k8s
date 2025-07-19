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
func DownloadCompatibilityArtifact(ctx context.Context, specRef string) (interface{}, error) {
	log.Printf("Downloading compatibility spec from: %s", specRef)

	// 1. Connect to the registry and resolve the manifest by its tag
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

	// 2. Fetch and parse the OCI Manifest itself
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

	// 3. Find the correct layer within the manifest
	log.Printf("Searching for spec layer with media type: %s or %s", types.CompatibilitySpecMediaType, types.ModelCompatibilitySpecMediaType)
	var specLayerDesc *ocispec.Descriptor
	for _, layer := range manifest.Layers {
		if layer.MediaType == types.CompatibilitySpecMediaType || layer.MediaType == types.ModelCompatibilitySpecMediaType {
			// Found it! Keep a pointer to this descriptor.
			specLayerDesc = &layer
			break
		}
	}

	if specLayerDesc == nil {
		return nil, fmt.Errorf("could not find a layer with a known compatibility spec media type (%s or %s)", types.ModelCompatibilitySpecMediaType, types.CompatibilitySpecMediaType)
	}
	log.Printf("Found spec layer with digest: %s", specLayerDesc.Digest)

	// 4. Fetch the content of the spec layer using its descriptor
	log.Println("Fetching compatibility spec content...")
	specBytes, err := content.FetchAll(ctx, repo, *specLayerDesc)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch spec layer content: %w", err)
	}

	// 4. Attempt to unmarshal as a ModelCompatibilitySpec first.
	var modelSpec types.ModelCompatibilitySpec
	modelErr := json.Unmarshal(specBytes, &modelSpec)
	if modelErr == nil {
		// It's very important to check that the core differentiating field is actually present,
		// as a simple spec might successfully unmarshal into the wrong struct with empty fields.
		if len(modelSpec.Compatibilities) > 0 && len(modelSpec.Compatibilities[0].Rules) > 0 {
			log.Println("Successfully parsed as types.ModelCompatibilitySpec")
			return &modelSpec, nil
		}
	}

	// 5. If the first attempt failed, attempt to unmarshal as the original CompatibilitySpec.
	var featureSpec types.CompatibilitySpec
	featureErr := json.Unmarshal(specBytes, &featureSpec)
	if featureErr == nil {
		if len(featureSpec.Compatibilities) > 0 && len(featureSpec.Compatibilities[0].Rules) > 0 {
			log.Println("Successfully parsed as types.CompatibilitySpec")
			return &featureSpec, nil
		}
	}

	// 6. If both attempts failed, the artifact is malformed.
	return nil, fmt.Errorf("failed to parse spec as either model-based or feature-based type. Model error: [%v]. Feature error: [%v]", modelErr, featureErr)

}
