# OCI distribution conformance

This directory runs the official OCI distribution-spec conformance suite
against `ociserver` backed by the registry implementations in this repository.

Docker must be installed and running. Run every backend with:

```console
task conformance
```

Individual backends can be selected with `task conformance:ocimem` or
`task conformance:ocilayout`.

The harness builds a pinned version of the upstream conformance runner, starts
each registry on a temporary local port, and fails when the upstream runner
reports a conformance failure. HTML, YAML, and JUnit reports are written under
`conformance/results/<backend>/`.
