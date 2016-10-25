
[![Build Status](https://travis-ci.org/aledbf/ingress-controller.svg?branch=master)](https://travis-ci.org/aledbf/ingress-controller)
[![Coverage Status](https://coveralls.io/repos/github/aledbf/ingress-controller/badge.svg?branch=master)](https://coveralls.io/github/aledbf/ingress-controller?branch=master)
[![Go Report Card](https://goreportcard.com/badge/github.com/aledbf/ingress-controller)](https://goreportcard.com/report/github.com/aledbf/ingress-controller)

# Ingress Controller

This project contains the boilerplate to spin up an Ingress controller.

See [Ingress controller documentation](https://github.com/kubernetes/contrib/blob/master/ingress/controllers/README.md) for details on how it works.

## DONE:
  - SSL pass through
  - avoid unnecessary overwrite of ssl certificates
  - [Interface](https://github.com/aledbf/ingress-controller/blob/master/pkg/ingress/types.go#L40)
  
## TODO:
  - api docs
  - example
  - used directories
  - on shutdown remove status IP
  - finish [first implementation](https://github.com/aledbf/ingress-controller/tree/master/backends/nginx) (nginx)
  - add ingress uuid (allow to group multiple controller in the same namespace - this is required to update the status IP in the leader)
  - 
