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

//go:embed historical/0.10.0/Repository.pkl
var historicalRepository0100 string

//go:embed historical/0.10.0/PackageScopeRepository.pkl
var historicalPackageScope0100 string

//go:embed historical/0.12.0/Repository.pkl
var historicalRepository0120 string

//go:embed historical/0.12.0/PackageScopeRepository.pkl
var historicalPackageScope0120 string

//go:embed historical/0.13.0/Repository.pkl
var historicalRepository0130 string

//go:embed historical/0.13.0/PackageScopeRepository.pkl
var historicalPackageScope0130 string
