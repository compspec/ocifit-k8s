<p align="center">
  <img src="docs/ocifit-k8s.png" alt="OCIFit Kubernetes">
</p>

This is a Kubernetes controller that will do the following:

1. Start running in a cluster with NFD, and retrieving metadata about nodes in the cluster, along with being updated when nodes are added and removed.
2. Receiving pods and checking if they are flagged for image selection.
  a. Being flagged means having the label "oci.image.compatibilities.selection/enabled" and (optionally) a node selector
  b. If the cluster is not homogenous, a node selector is required, and should be the instance type that the pod is intended for.
  c. If enabled, a URI is provided that points to a compatibility artifact
  d. The artifact describes several images (and criteria for checking) that can be used for the Pod
  e. The controller checks known nodes for the instance type against the spec, 


## Notes

For the above, you don't technically need NFD if you add and select your own labels, but I want to use NFD to better integrate with the larger 
community. The controller webhook will work with or without it, assuming the labels you have on nodes that you are selecting for exist.
Next, we place this controller on the level of a webhook, meaning that it isn't influencing scheduling, but is anticipating what container
should be selected for a node based on either:

1. A homogenous cluster - "All nodes are the same, match the container image to it"
2. A nodeSelector - "We are going to tell the Kubernetes scheduler to match to this node, so select the container for it"

It would be another design to make a custom scheduler plugin to select a container based on a node selected. I chose
this approach because it places the decision point before we have sent anything to the scheduler. The other approach has feet
but needs further discussion and thinking.

## Labels and Annotations


| Name | Type | Default | Required | Description |
|------|------|---------|----------|-------------|
| oci.image.compatibilities.selection/target-image | annotation | placeholder:latest | yes | image URI to replace in pod |
| oci.image.compatibilities.selection/image-ref| annotation | placeholder:latest | no | artifact reference "image" in OCI registry |
| oci.image.compatibilities.selection/enabled | label | unset | yes | Flag to indicate we want to do compatibility image selection |

Note that if you remove enabled, the webhook won't trigger, so it is required.

## Developer

You'll need to build the main controller (webhook) image:

```bash
make
```

You can either push to a registry, or load into kind.

```bash
# Push to registry, you likely want to tweak the Makefile URI
make push

# Load into kind
kind load docker-image ghcr.io/compspec/ocifit-k8s:latest
```

And install the deployment manifest (assuming you are sitting in the cloned repository)

```bash
kubectl apply -f deploy/webhook.yaml

# The same
make install
```

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
In our real world use case we would select based on operating system and kernel version. For our test case, we will just use a script that will programaticallly update worker nodes. In this example,
we are just going to add the same label to all nodes and then check our controller based on the image selected. Let's first add "vanilla":

```bash
bash ./example/add-nfd-features.sh "compspec.ocifit-k8s.flavor=vanilla"
```

View our added labels:

```bash
$ kubectl get nodes -o json | jq .items[].metadata.labels | grep compspec.ocifit-k8s.flavor
```
```console
  "compspec.ocifit-k8s.flavor": "vanilla",
  "compspec.ocifit-k8s.flavor": "vanilla",
  "compspec.ocifit-k8s.flavor": "vanilla",
```

If you want to see actual NFD labels:

```bash
kubectl -n node-feature-discovery get all
kubectl get nodes -o json | jq '.items[].metadata.labels'
```

### 3. Install OCIFit 

OCIFit is a mutating webhook controller that will work with NFD (or labels of your choosing on nodes) to select the container image for a pod that
is enabled for it. You can look at examples in [example/pods](example/pods) for the labels we need to support the above. The cool thing is that
for a homogenous cluster, we don't need to add a nodeSelector label. We can figure out how to select a container based on whatever node it 

```bash
kubectl apply -f deploy/webhook.yaml

# The same
make install
```

Check the logs to make sure it is running. You should see it running the homogeneity check (this is done on updates, etc)

```bash
kubectl logs ocifit-k8s-deployment-68d5bf5865-494mg -f
```

### 4. Test Compatibilility

At this point, we want to test compatibility. This step is already done, but I'll show you how I designed the compatibility spec. The logic for this dummy case is the following:

1. If our custom label "compspec.ocifit-k8s.flavor" is vanilla, we want to choose a debian container.
1. If our custom label "compspec.ocifit-k8s.flavor" is chocolate, we want to choose a ubuntu container.
1. If our custom label "compspec.ocifit-k8s.flavor" is strawberry, we want to choose a rockylinux container.

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