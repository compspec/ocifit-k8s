package flux

import (
	"log"
	"strings"

	"ghcr.io/compspec/ocifit-k8s/pkg/types"
)

// GetPatches takes the final image and generates patches to the Flux object
func GetPatches(
	rawObject map[string]interface{},
	prediction *types.PredictionResponse,
) []types.JSONPatch {
	var patches []types.JSONPatch

	// If we get the flux operator, help the user by setting the correct view.
	// Rocky should work even with Ubuntu, but ubuntu 22.04 won't work with 24.04
	fluxViewImage := "ghcr.io/converged-computing/flux-view-rocky:tag-9"
	if prediction.Arch == "arm64" {
		fluxViewImage = "ghcr.io/converged-computing/flux-view-rocky:arm-9"
	}

	// Get the 'spec' field from the raw map.
	specMap, ok := rawObject["spec"].(map[string]interface{})
	if !ok {
		// A valid k8s object MUST have a spec, but this is a safe check.
		// We'll create it if it's missing.
		patches = append(patches, types.JSONPatch{
			Op:   "add",
			Path: "/spec",
			Value: map[string]interface{}{
				"flux": map[string]interface{}{
					"container": map[string]interface{}{
						"image": fluxViewImage,
					},
				},
			},
		})

	} else {
		fluxMap, fluxKeyExists := specMap["flux"].(map[string]interface{})

		if !fluxKeyExists {
			// The flux key does not exist in the original JSON so add it
			log.Println("Patching MiniCluster: path /spec/flux does not exist. Adding.")
			patches = append(patches, types.JSONPatch{
				Op:   "add",
				Path: "/spec/flux",
				Value: map[string]interface{}{
					"container": map[string]interface{}{
						"image": fluxViewImage,
					},
				},
			})
		} else {
			// flux exists, what about container?
			_, containerKeyExists := fluxMap["container"].(map[string]interface{})
			if !containerKeyExists {
				log.Println("Patching MiniCluster: path /spec/flux/container does not exist. Adding.")
				patches = append(patches, types.JSONPatch{
					Op:   "add",
					Path: "/spec/flux/container",
					Value: map[string]interface{}{
						"image": fluxViewImage,
					},
				})
			} else {
				// Both '/spec/flux' and '/spec/flux/container' are guaranteed to exist.
				log.Println("Patching MiniCluster: path /spec/flux/container/image will be added/replaced.")
				patches = append(patches, types.JSONPatch{
					Op:    "add",
					Path:  "/spec/flux/container/image",
					Value: fluxViewImage,
				})
			}
		}
	}

	// A node selector will always be provided (at least for now)!
	if !ok {

		// I'm not sure the order these are applied, so let's create the spec if not OK
		log.Println("Patching MiniCluster: /spec does not exist. Adding with pod and nodeSelector.")
		patches = append(patches, types.JSONPatch{
			Op:   "add",
			Path: "/spec",
			Value: map[string]interface{}{
				"pod": map[string]interface{}{
					"nodeSelector": map[string]string{
						prediction.InstanceSelector: prediction.Instance,
					},
				},
			},
		})
	} else {
		podMap, ok := specMap["pod"].(map[string]interface{})
		if !ok {
			log.Println("Patching MiniCluster: /spec/pod does not exist. Adding with nodeSelector.")
			patches = append(patches, types.JSONPatch{
				Op:   "add",
				Path: "/spec/pod",
				Value: map[string]interface{}{
					"nodeSelector": map[string]string{
						prediction.InstanceSelector: prediction.Instance,
					},
				},
			})

		} else {
			_, nodeSelectorKeyExists := podMap["nodeSelector"].(map[string]interface{})
			if !nodeSelectorKeyExists {
				// 'nodeSelector' key is missing. Add it.
				log.Println("Patching MiniCluster: /spec/pod/nodeSelector does not exist. Adding.")
				patches = append(patches, types.JSONPatch{
					Op:   "add",
					Path: "/spec/pod/nodeSelector",
					Value: map[string]string{
						prediction.InstanceSelector: prediction.Instance,
					},
				})
			} else {

				// The full path to the nodeSelector map exists. Add/replace the specific label.
				// According to RFC 6902, '/' in a key must be escaped to '~1' (weird)
				escapedLabel := strings.ReplaceAll(prediction.Instance, "/", "~1")
				log.Printf("Patching MiniCluster: Adding label to existing /spec/pod/nodeSelector.")
				patches = append(patches, types.JSONPatch{
					Op:    "add",
					Path:  "/spec/pod/nodeSelector/" + escapedLabel,
					Value: prediction.Instance,
				})
			}
		}
	}
	return patches
}
