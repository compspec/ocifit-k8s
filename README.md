# OCIFit-k8s

This is a Kubernetes controller that will do the following:

1. Start running in a cluster with NFD, and retrieving metadata about nodes in the cluster, along with being updated when nodes are added and removed.
2. Receiving pods and checking if they are flagged for image selection.
  a. Being flagged means having the label "oci.image.compatibilities.selection/enabled" and (optionally) a node selector
  b. If the cluster is not homogenous, a node selector is required, and should be the instance type that the pod is intended for.
  c. If enabled, a URI is provided that points to a compatibility artifact
  d. The artifact describes several images (and criteria for checking) that can be used for the Pod
  e. The controller checks known nodes for the instance type against the spec, 


## Usage

### 1. Prepare Cluster

Create a cluster with kind (or your cloud of choice). If you want to create a test cluster:

```bash
kind create cluster --config ./example/kind-config.yaml
```

Then, install cert-manager.

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.17.2/cert-manager.yaml
```

And install NFD. This will add node feature discovery labels to each node.


```bash
kubectl apply -k https://github.com/kubernetes-sigs/node-feature-discovery/deployment/overlays/default?ref=v0.17.3
```

### 2. Add Custom Labels

Since we want to test that NFD is working, we are going to add custom labels. We just want to test and don't need the labels to persist with recreations, so we can just use `kubectl label`. However,
if we do it the right (persistent) way we would write a configuration file to `/etc/kubernetes/node-feature-discovery/features.d` on the node.
In our real world use case we would select based on operating system and kernel version. We have a script that will programaticallly update worker nodes. I chose
an arbitrary new label that is just the worker name.

```bash
bash ./example/add-nfd-features.sh
```

View our added labels:

```bash
$ kubectl get nodes -o json | jq .items[].metadata.labels | grep compspec.ocifit-k8s.node
```
```console
  "compspec.ocifit-k8s.node": "kind-worker",
  "compspec.ocifit-k8s.node": "kind-worker2",
  "compspec.ocifit-k8s.node": "kind-worker3",
```

If you want to see actual NFD labels:

```bash
kubectl -n node-feature-discovery get all
kubectl get nodes -o json | jq '.items[].metadata.labels'
```

### 3. Test Compatibilility

At this point, we want to test compatibility. This step is already done, but I'll show you how I designed the compatibility spec. The logic for this dummy case is the following:

1. If our custom label "compspec.ocifit-k8s.node" is for kind-worker, we want to choose a debian container.
2. If our custom label "compspec.ocifit-k8s.node" is for kind-worker2, we want to choose an ubuntu container.
3. If our custom label "compspec.ocifit-k8s.node" is for kind-worker3, we want to choose a rockylinux container.

Normally, we would attach a compatibility spec to an image, like [this](https://github.com/kubernetes-sigs/node-feature-discovery/blob/master/docs/usage/image-compatibility.md#attach-the-artifact-to-the-image). But
here we are flipping the logic a bit. We don't know the image, and instead we are directing the client to look directly at one artifact. Thus, the first step was to package the compatibility artifact and push to a registry (and make it public). I did that as follows (you don't need to do this):

```bash
oras push ghcr.io/compspec/ocifit-k8s-compatibility:kind-example ./example/compatibility-test.json:application/vnd.oci.image.compatibilities.v1+json
```

We aren't going to be using any referrers API or linking this to an image. The target images are in the artifact.

**being written**

## License

HPCIC DevTools is distributed under the terms of the MIT license.
All new contributions must be made under this license.

See [LICENSE](https://github.com/converged-computing/cloud-select/blob/main/LICENSE),
[COPYRIGHT](https://github.com/converged-computing/cloud-select/blob/main/COPYRIGHT), and
[NOTICE](https://github.com/converged-computing/cloud-select/blob/main/NOTICE) for details.

SPDX-License-Identifier: (MIT)

LLNL-CODE- 842614