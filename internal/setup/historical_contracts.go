package setup

import _ "embed"

//go:embed historical/0.9.0/Repository.pkl
var historicalRepository090 string

//go:embed historical/0.9.0/PackageScopeRepository.pkl
var historicalPackageScope090 string

//go:embed historical/0.9.0/Verifier.pkl
var historicalVerifier090 string

//go:embed historical/0.9.0/Focus.pkl
var historicalFocus090 string
