# Third-party CRDs

The backup CRD is extracted unchanged from the [Percona operator chart 1.20.0](https://github.com/percona/percona-helm-charts/blob/pxc-operator-1.20.0/charts/pxc-operator/crds/crd.yaml).
We keep the upstream schema so local controller tests exercise the operator's served API shape without importing its Go module.
