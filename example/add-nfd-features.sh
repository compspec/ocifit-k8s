#!/bin/bash

# Programmatically add a custom feature file to NFD worker pods
# running on all non-control-plane nodes in a Kubernetes cluster.

set -oe pipefail

# --- Configuration ---
NFD_NAMESPACE="node-feature-discovery"

# This is just a prefix - we will add an arbitrary selector here
FEATURE_LABEL=${1:-"compspec.ocifit-k8s.flavor=vanilla"}
echo "Planning to add ${FEATURE_LABEL} to worker nodes..."

echo "Finding worker nodes (nodes without the control-plane role)..."
WORKER_NODES=$(kubectl get nodes -l '!node-role.kubernetes.io/control-plane' -o jsonpath='{.items[*].metadata.name}')

if [ -z "$WORKER_NODES" ]; then
    echo "No worker nodes found. Exiting."
    exit 0
fi

echo "Found worker nodes: ${WORKER_NODES}"
echo "---"


# Loop through each worker node name.
for node_name in $WORKER_NODES; do
    echo "Processing node: ${node_name}"
    echo "  -> Applying labels..."
    kubectl label node "${node_name}" "${FEATURE_LABEL}" --overwrite
    echo "  -> Successfully applied labels to node '${node_name}'."
    echo "---"
done

echo "All worker nodes have been labeled."
echo "You can verify by running: kubectl get nodes --show-labels"