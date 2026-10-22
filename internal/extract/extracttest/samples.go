package extracttest

// RunnerConfig is a runner config whose resource keys were written below the
// [runners.kubernetes.node_selector] header. TOML puts a key in the table of
// the last header above it, so the keys became node-selector labels, with no
// error, and no node carries those labels. At the path the author meant, the
// extractor finds nothing; the keys are under the header above them.
const RunnerConfig = `concurrent = 4

[[runners]]
  name = "k3s"
  [runners.kubernetes]
    namespace = "gitlab-runner"
    pull_policy = "if-not-present"
    [runners.kubernetes.node_selector]
      "kubernetes.io/hostname" = "builder"
    cpu_limit = "2"
    memory_limit = "3Gi"
`

// Manifests is a YAML file of three documents: two Deployments, web and
// worker, and a Service named web, each with a replica count.
const Manifests = `---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  replicas: 2
---
kind: Deployment
metadata:
  name: worker
spec:
  replicas: 3
---
kind: Service
metadata:
  name: web
spec:
  replicas: 9
---
`
