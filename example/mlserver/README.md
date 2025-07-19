# MLServer Testing on EKS

Let's test the MLServer on EKS. Create the one node cluster:

```bash
eksctl create cluster --config-file ./eks-config-hpc6a.48xlarge.yaml 
aws eks update-kubeconfig --region us-east-2 --name ocifit-test
```

## Developer

You'll need to build the main controller (webhook) image:

```bash
make
make push
```

And the mlserver image.

```
make mlserver
make mlserver-push
```

You'll need the certificate manager.

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.17.2/cert-manager.yaml
```

And install the deployment manifest (assuming you are sitting in the cloned repository)

```bash
kubectl apply -f deploy/webhook-with-mlserver.yaml
```

Make sure everything is running:

```bash
$ kubectl get pods
NAME                                     READY   STATUS    RESTARTS   AGE
ocifit-k8s-deployment-86c7fcbbfb-nfs52   2/2     Running   0          39s
```

Install the flux operator and node feature discovery.

```bash
kubectl apply -f https://raw.githubusercontent.com/flux-framework/flux-operator/refs/heads/main/examples/dist/flux-operator.yaml
kubectl apply -k https://github.com/kubernetes-sigs/node-feature-discovery/deployment/overlays/default?ref=v0.17.3
```

## ML Server Decision

Now create a simple pod that will be decided by the model:

```bash
kubectl apply -f pod.yaml

# When you are done
kubectl delete -f pod.yaml
```

Try creating an hpcg run with a manual yaml:

```bash
kubectl apply -f minicluster.yaml

# When you are done
kubectl delete -f minicluster.yaml
```

Orchestrate with helm.

```bash
git clone https://github.com/converged-computing/flux-apps-helm
cd flux-apps-helm
helm dependency update hpcg-matrix/
helm install \
  --set experiment.nodes=1 \
  --set minicluster.size=1 \
  --set minicluster.tasks=$NPROC \
  --set experiment.tasks=$NPROC \
  --set minicluster.save_logs=true \
  --set minicluster.image=placeholder:latest \
  --set experiment.iterations=3 \
  --set "label.oci\.image\.compatibilities\.selection/enabled"=true \
  --set "annotation.oci\.image\.compatibilities\.selection/model"=fom \
  --set "annotation.oci\.image\.compatibilities\.selection/image-ref"=ghcr.io/compspec/ocifit-k8s-compatibility:ml-example \
  hpcg ./hpcg-matrix
```

That's it! We next would want to test this with a cluster that has autoscaling, and if there aren't nodes to start, we'd start with a cache of labels. When you are done:

```bash
eksctl delete cluster --config-file ./eks-config-hpc6a.48xlarge.yaml  --wait
```

## License

HPCIC DevTools is distributed under the terms of the MIT license.
All new contributions must be made under this license.

See [LICENSE](https://github.com/converged-computing/cloud-select/blob/main/LICENSE),
[COPYRIGHT](https://github.com/converged-computing/cloud-select/blob/main/COPYRIGHT), and
[NOTICE](https://github.com/converged-computing/cloud-select/blob/main/NOTICE) for details.

SPDX-License-Identifier: (MIT)

LLNL-CODE- 842614
