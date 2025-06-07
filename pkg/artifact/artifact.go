package artifact

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"ghcr.io/compspec/ocifit-k8s/pkg/types"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
)

// DownloadCompatibilityArtifact and return the parsed spec
func DownloadCompatibilityArtifact(ctx context.Context, specRef string) (*types.CompatibilitySpec, error) {
	log.Printf("Downloading compatibility spec from: %s", specRef)

	// Use ORAS to fetch the artifact
	reg, err := remote.NewRegistry(strings.Split(specRef, "/")[0])
	if err != nil {
		return nil, fmt.Errorf("failed to connect to registry for spec: %w", err)
	}

	repoName := strings.SplitN(strings.TrimPrefix(specRef, reg.Reference.Host()), ":", 2)[0]
	repoName = strings.TrimPrefix(repoName, "/") // Clean up repo name
	tag := strings.Split(specRef, ":")[1]

	repo, err := reg.Repository(ctx, repoName)
	if err != nil {
		return nil, fmt.Errorf("failed to access repository %s: %w", repoName, err)
	}

	desc, err := repo.Resolve(ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve spec artifact %s: %w", specRef, err)
	}

	specBytes, err := content.FetchAll(ctx, repo, desc)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch spec artifact content: %w", err)
	}

	// Unmarshal the JSON into our struct
	var spec types.CompatibilitySpec
	if err := json.Unmarshal(specBytes, &spec); err != nil {
		return nil, fmt.Errorf("failed to unmarshal compatibility spec JSON: %w", err)
	}

	log.Printf("Successfully downloaded and parsed spec version %s", spec.Version)
	return &spec, nil
}
