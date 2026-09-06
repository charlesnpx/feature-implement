# Third-Party Notices

The repository does not vendor third-party source or assets. The `feature`
runtime binary imports these Go modules:

- `golang.org/x/sys` v0.47.0, BSD-3-Clause, copyright 2009 The Go Authors.
- `golang.org/x/text` v0.40.0, BSD-3-Clause, copyright 2009 The Go Authors.
- `gopkg.in/yaml.v3` v3.0.1, MIT for libyaml-derived files and Apache-2.0
  for the remaining project files.
- `github.com/charlesnpx/witness`, MIT.

The `gopkg.in/yaml.v3` NOTICE file states:

```text
Copyright 2011-2016 Canonical Ltd.
```

If future releases vendor or embed additional third-party material, add the
required upstream license and notice text here before shipping that release.
