# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- Migrate to App Build Suite (ABS).

## [0.6.0] - 2025-10-08

### Removed

- Remove code that took care of removing finalizer from CAPA CRs.

## [0.5.0] - 2025-10-07

### Changed

- Remove finalizer from reconciled CAPA CRs.

## [0.4.0] - 2025-10-01

### Changed

- Change container image registry in Makefile to `gsoci.azurecr.io`
- Use static `finalizer` instead of creating a dynamic `finalizer` based on the rule contents.

## [0.3.1] - 2024-11-07

### Changed

- Disable logger development mode to avoid panicking

## [0.3.0] - 2024-07-25

### Changed

- Bug: bump protobuf module to avoid CVE-2024-24786.
- Update renovate to json5 config.
- Upgrade `k8s.io/client-go` and `k8s.io/apimachinery` from `0.27.2` to `0.29.2`
- Upgrade `sigs.k8s.io/controller-runtime` from `0.15.1` to `0.17.3`

## [0.2.0] - 2024-01-17

### Changed

- Configure `gsoci.azurecr.io` as the default container image registry.
- Add switch to PSPs for 1.25.

## [0.1.4] - 2023-08-24

### Changed

- Push to Vsphere and Cloud Director app collection. Don't push to Openstack app collection.
- Re-ignore CVE-2020-8561
- Add ignore for CVE-2023-29401
- Fix security issues reported by kyverno policies.

## [0.1.3] - 2023-03-16

### Added

- Add the use of the runtime/default seccomp profile

### Changed

- Allow required volume types in PSP

## [0.1.2] - 2022-08-05

## [0.1.1] - 2022-06-28

### Added

- Push to GCP app collection

## [0.1.0] - 2022-06-27

### Added

- Project initilization.

[Unreleased]: https://github.com/giantswarm/deletion-blocker-operator/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/giantswarm/deletion-blocker-operator/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/giantswarm/deletion-blocker-operator/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/giantswarm/deletion-blocker-operator/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/giantswarm/deletion-blocker-operator/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/giantswarm/deletion-blocker-operator/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/giantswarm/deletion-blocker-operator/compare/v0.1.4...v0.2.0
[0.1.4]: https://github.com/giantswarm/deletion-blocker-operator/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/giantswarm/deletion-blocker-operator/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/giantswarm/deletion-blocker-operator/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/giantswarm/deletion-blocker-operator/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/giantswarm/deletion-blocker-operator/releases/tag/v0.1.0
