#!/bin/bash
set -e

# List the label keys to remove, each followed by a minus sign.
LABELS_TO_REMOVE="feature.node.ocifit-k8s.flavor-"

echo "Finding worker nodes..."
WORKER_NODES=$(kubectl get nodes -l '!node-role.kubernetes.io/control-plane' -o jsonpath='{.items[*].metadata.name}')

if [ -z "$WORKER_NODES" ]; then
    echo "No worker nodes found. Exiting."
    exit 0
fi

for node_name in $WORKER_NODES; do
    echo "Removing labels from node: ${node_name}"
    kubectl label node "${node_name}" ${LABELS_TO_REMOVE} || true
done

echo "Cleanup complete."