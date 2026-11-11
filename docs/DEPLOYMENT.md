# Deploying Tremor for free

Two paths. **Local (kind)** needs only Docker and costs nothing. **Public demo** runs k3s on an Oracle Cloud Always Free VM, stores run history in MongoDB Atlas M0 and pulls images from GitHub Container Registry (GHCR). All of it is free tier; check each provider's current limits before you rely on them.

| Piece | Local | Public demo |
| --- | --- | --- |
| Cluster | kind, 1 control plane + 2 workers | k3s, 1-2 Oracle Always Free VMs |
| Images | built locally and loaded | GHCR (published by the `release` workflow) |
| Run history | built-in MongoDB (ephemeral) | MongoDB Atlas M0 |
| Install | `make kind-deploy` | `helm install` |

## 1. Local, on kind

```powershell
kind create cluster --name tremor --config hack/kind-config.yaml
make kind-deploy        # operator + history MongoDB   (or: helm install, see below)
make demo-deploy        # demo shop + its MongoDB
make netem-load         # image for network faults
kubectl apply -f config/samples/shop-pod-kill.yaml
kubectl -n demo get chaosexperiments
```

Without `make` (Windows), run the commands inside the Makefile targets, or use the Helm chart:

```powershell
docker build -t tremor-manager:dev .
docker build -t tremor-netem:dev -f Dockerfile.netem .
kind load docker-image tremor-manager:dev tremor-netem:dev --name tremor
helm install tremor deploy/helm/tremor -n tremor-system --create-namespace `
  --set image.repository=tremor-manager --set image.tag=dev `
  --set netemImage=tremor-netem:dev --set 'allowedNamespaces={demo}'
```

## 2. Release the images (once)

Images are built by `.github/workflows/release.yml` when you push a version tag, for `linux/amd64` and `linux/arm64` (Oracle's free Ampere VMs are arm64).

```powershell
git tag v0.8.0
git push origin v0.8.0
```

Then, on GitHub, open your profile, Packages, and set `tremor-manager`, `tremor-shop` and `tremor-netem` to **public** so the cluster can pull them without credentials. The workflow also attaches `tremor-0.8.0.tgz` (the Helm chart) to the GitHub release.

## 3. A free cluster on Oracle Cloud

1. Create an Oracle Cloud account (Always Free). Pick a home region with Ampere A1 capacity; capacity varies and the shape sometimes shows "out of capacity", in which case retry later or use the AMD micro shape (too small for the demo shop plus operator; prefer Ampere).
2. Create an instance: shape `VM.Standard.A1.Flex` (up to 4 OCPU / 24 GB within the free allowance), image Ubuntu 22.04 or 24.04, add your SSH key.
3. In the VCN security list, allow inbound TCP 22 (SSH) and 6443 only from your own IP if you want to run `kubectl` from your laptop. Do not open the metrics or Grafana ports to the world.
4. SSH in and install k3s:

```bash
curl -sfL https://get.k3s.io | sh -
sudo kubectl get nodes
```

5. Network faults need the `sch_netem` kernel module on the node:

```bash
sudo apt-get update && sudo apt-get install -y "linux-modules-extra-$(uname -r)"
sudo modprobe sch_netem && lsmod | grep netem
```

6. Copy `/etc/rancher/k3s/k3s.yaml` to your laptop as `~/.kube/config`, replacing `127.0.0.1` with the VM's public IP. A free VM has one node, so `NodeDrain` is refused (nowhere for pods to go); add a second Always Free VM as a k3s agent if you want to try it.

## 4. Run history in MongoDB Atlas M0

1. Create a free M0 cluster at mongodb.com/atlas, a database user, and allow the VM's public IP in Network Access.
2. Copy the `mongodb+srv://` connection string and store it as a Secret so the password never lands in Helm values:

```bash
kubectl create namespace tremor-system
kubectl -n tremor-system create secret generic tremor-history \
  --from-literal=uri='mongodb+srv://USER:PASSWORD@CLUSTER.mongodb.net/?retryWrites=true&w=majority'
```

## 5. Install Tremor

```bash
helm install tremor https://github.com/Amitreddy14/tremor/releases/download/v0.8.0/tremor-0.8.0.tgz \
  -n tremor-system \
  --set history.mode=external --set history.existingSecret=tremor-history \
  --set 'allowedNamespaces={demo}' \
  --set clusterName=oracle-free
kubectl -n tremor-system rollout status deploy/tremor
```

The chart installs the two CRDs, RBAC, the operator and a metrics Service. Helm installs CRDs once and never upgrades them, so after a chart upgrade apply new CRDs yourself: `kubectl apply -f deploy/helm/tremor/crds/`.

Deploy the demo shop from GHCR and run an experiment:

```bash
git clone https://github.com/Amitreddy14/tremor && cd tremor
kubectl apply -k config/demo-ghcr
kubectl -n demo rollout status deploy/shop
kubectl apply -f config/samples/shop-pod-kill.yaml
kubectl -n demo get chaosexperiments -w
```

Edit `newTag` in `config/demo-ghcr/kustomization.yaml` if you released a different version.

## 6. Dashboards (optional)

Grafana plus Prometheus is heavy for a free VM (kube-prometheus-stack wants about 1 GB). Run it on kind instead, or install the stack with trimmed resources (`hack/kps-values.yaml`) and then:

```bash
helm upgrade tremor <chart> -n tremor-system --reuse-values \
  --set metrics.serviceMonitor.enabled=true --set metrics.serviceMonitor.labels.release=kps
```

Never expose Grafana to the internet without authentication; use `kubectl port-forward`.

## 7. Reading results

```bash
kubectl -n tremor-system port-forward deploy/tremor 8080:8080   # metrics
tremorctl history --mongo-uri "$URI"                            # past runs
```

## Safety on a shared cluster

Always set `allowedNamespaces`. Leave `forceDryRun=true` until you have checked the targets in the dry-run output. Keep the metrics endpoint inside the cluster. Experiments disturb real pods: aim them at the demo namespace.

## Cost check

Oracle Always Free, Atlas M0 and GHCR (public packages) cost nothing today. Always Free instances can be reclaimed if idle for a long time, and the free allowance can change, so treat the demo as disposable: everything is reproducible from the chart and the samples.
