# Changelog

All notable changes to Horizon will be documented in this file.

## [0.1.7] - 2026-03-27

### 📚 Documentation

- Update CHANGELOG.md for v0.1.6
([ebd4da8](https://github.com/darioajr/horizon/commit/ebd4da823fe6d87e1cc059ec5bef49aee42c0b48))

### 🚀 Features

- Add UPX compression for binaries and support for multiple storage backends
([87006b3](https://github.com/darioajr/horizon/commit/87006b3079f732b29cd321cdf23d91b983d0d3c9))
- Add .golangci.yml configuration for build tags
([0dcfccc](https://github.com/darioajr/horizon/commit/0dcfccc671d16ef039a580fb5db3ad3a3dfb471a))

## [0.1.6] - 2026-03-17

### 📚 Documentation

- Update CHANGELOG.md for v0.1.5
([e6c4799](https://github.com/darioajr/horizon/commit/e6c47996a3f5bb8a778f70337984175a541c423a))

### 📝 Other

- Merge branch 'main' of github.com:darioajr/horizon
([69cea19](https://github.com/darioajr/horizon/commit/69cea19460724b93ef1b04f1c78557c62284d7c0))

### 🚀 Features

- Add build script for Linux/macOS with multiple targets and Docker support
([7a56770](https://github.com/darioajr/horizon/commit/7a56770a715ea966ea35e6ed6172b40f971a9973))
- Add container runtime detection for Docker and Podman in build scripts
([16f0d48](https://github.com/darioajr/horizon/commit/16f0d482b02a5d0ab640dd28c6aefe4ba9f35905))
- Update documentation to support Podman alongside Docker
([b0738cc](https://github.com/darioajr/horizon/commit/b0738cc97240dbca189ead4e6d4ff5235daecb5f))
- Implement adaptive write coalescing with accumulator for improved performance
([365ef0f](https://github.com/darioajr/horizon/commit/365ef0f6dbe3abf3f37cfc1f88464da4102b228a))

## [0.1.5] - 2026-03-04

### 🐛 Bug Fixes

- Specify golangci-lint version to v1.64.8 for consistency
([a5d4b20](https://github.com/darioajr/horizon/commit/a5d4b20ebd94ba28ac4340f3b17675898665be59))

### 📚 Documentation

- Update CHANGELOG.md for v0.1.4
([c034378](https://github.com/darioajr/horizon/commit/c034378858b1541fb944ac3c56d8667ec06bfb6f))

## [0.1.4] - 2026-03-04

### ♻️ Refactoring

- Update produce_benchmark.py to support remote brokers and improve latency tracking
([6c96634](https://github.com/darioajr/horizon/commit/6c96634ee033b4074dd7f0731d40abf8ebe3143f))

### 📚 Documentation

- Update CHANGELOG.md for v0.1.3
([a4b788c](https://github.com/darioajr/horizon/commit/a4b788c5f4ef0433149730df4fd008f62071f1b9))

### 📝 Other

- Merge branch 'main' of github.com:darioajr/horizon
([1924dfe](https://github.com/darioajr/horizon/commit/1924dfef65eaefd7ac4d8e304e0fcba529dd6670))

## [0.1.3] - 2026-03-04

### 📚 Documentation

- Update CHANGELOG.md for v0.1.2
([0bba848](https://github.com/darioajr/horizon/commit/0bba848fcc97954787dd1f8cf7ea165d18ce60d1))

### 📦 Miscellaneous

- Update Go version to 1.26 across workflows, documentation, and Docker configurations
([8514416](https://github.com/darioajr/horizon/commit/8514416ce4a9cd64c97fbcaffc6eab001cbe8608))

## [0.1.2] - 2026-03-04

### 📚 Documentation

- Update CHANGELOG.md for v0.1.1
([5bb4372](https://github.com/darioajr/horizon/commit/5bb4372bb0628b1b5d80a6ae4e4e2a576f8f8990))

### 📝 Other

- First commit
([8ff6044](https://github.com/darioajr/horizon/commit/8ff604408ce1c03e32d6bfad9ab7b0ecfdfe45eb))
- Add benchmarking framework for Horizon vs Apache Kafka

- Introduced Apache License 2.0 for project compliance.
- Added .gitignore for benchmark results and Python artifacts.
- Created Dockerfile for building the OpenMessaging Benchmark framework.
- Implemented Horizon and Kafka driver configurations for benchmarking.
- Developed a Python script to benchmark message production performance.
- Included requirements.txt for necessary Python packages.
- Defined workloads for latency and throughput tests with 1KB messages.
([baa31a6](https://github.com/darioajr/horizon/commit/baa31a67e005c314a14404dc23a14387ea8de1c0))
- Implement storage engine interfaces for multiple backends

- Added a new storage factory in `internal/storage/factory.go` to create storage engines for different backends: file, S3, Redis, and Infinispan.
- Implemented the Infinispan storage engine in `internal/storage/infinispan/infinispan.go` with methods for topic management, partition access, and data operations.
- Created a Redis-backed storage engine in `internal/storage/redis/redis.go`, supporting similar functionalities as Infinispan.
- Developed an S3 storage engine in `internal/storage/s3/s3.go`, designed to handle data storage in S3-compatible object stores.
- Defined common interfaces for storage engines in `internal/storage/interfaces.go`, ensuring all implementations adhere to the same contract.
([3868b39](https://github.com/darioajr/horizon/commit/3868b395cd7597c9706bf7f7b44481c8f6a18d73))
- Implement cluster management and HTTP gateway for Horizon

- Added cluster package for managing cluster membership, node states, and partition assignments.
- Introduced NodeState and NodeInfo structures to represent the state and information of nodes in the cluster.
- Implemented ClusterState to maintain the global view of the cluster, including methods for node and partition management.
- Created HTTP server package to provide a RESTful interface for producing messages, managing topics, and health checks.
- Implemented endpoints for producing messages, listing topics, retrieving topic metadata, and managing topic configurations.
- Added support for compression and content type handling in the HTTP produce endpoint.
- Included error handling and response formatting for HTTP requests.
([a8282b6](https://github.com/darioajr/horizon/commit/a8282b616bcb6ba9d80fb479549486504611b761))
- Add CI and release workflows, enhance Docker build support for multi-platform
([29a4bb6](https://github.com/darioajr/horizon/commit/29a4bb68cd50e0aaedcdee9d6ad8513d46dafe2f))
- Refactor log sync and deadline handling, improve error handling in connection setup
([8d2ae9b](https://github.com/darioajr/horizon/commit/8d2ae9b4cc250dbaaca33494abd788a07180d7ec))
- Refactor partition selection logic and remove unused code in cluster and server components
([f8dbf78](https://github.com/darioajr/horizon/commit/f8dbf78365c37de43c10f0e7fca1b66a4a28292b))
- Enhance release workflow: add changelog generation, update CHANGELOG.md, and improve badge visibility in README
([311efe3](https://github.com/darioajr/horizon/commit/311efe3007c7c1841c00b723408281f441e2051c))
- Update README.md: add FOSSA badge and reorganize contributing section
([8b7a682](https://github.com/darioajr/horizon/commit/8b7a68260b08b03334ae9b3a20444f3762e427a0))
- Update FOSSA badge in README.md to point to the correct project
([eef85d8](https://github.com/darioajr/horizon/commit/eef85d8149a0d4b5ee30436c29e95b502f0356fd))
- Add Docker usage instructions and configuration examples to README.md
([7c79403](https://github.com/darioajr/horizon/commit/7c794030bb2cfc45be33f2758009623b79ecb1f6))
- Enhance documentation and configuration for Docker usage

- Add environment variable configuration details to README.md
- Update configuration reference to include environment variables
- Introduce a comparative testing guide for Horizon vs Kafka in new testing-horizon-vs-kafka.md
- Improve examples and explanations for Docker usage in configuration.md
([2b7e200](https://github.com/darioajr/horizon/commit/2b7e20045d6d20d5cca53c509f50f0c8445fc61f))
- Fix remote URL macro in changelog configuration and set GitHub owner
([c666ce4](https://github.com/darioajr/horizon/commit/c666ce4660b9e43bcfbab8f1765c6f622ceec6a8))
- Remove unnecessary outputs from changelog job in release workflow
([c8dc1bf](https://github.com/darioajr/horizon/commit/c8dc1bf1005b710fd65210b0d35907cc7579b7ba))
- Merge branch 'main' of github.com:darioajr/horizon
([b4dfe6a](https://github.com/darioajr/horizon/commit/b4dfe6af5d2d4afbe0f179d9a9ef09bb34f185fa))

### 🚀 Features

- Implement DescribeConfigs handler and update Kafka container version
- Added handleDescribeConfigs function to process DescribeConfigs requests.
    - Updated Kafka container version from 7.5.0 to 8.1.1 in benchmarks.
    - Adjusted various API version handling and error messages for clarity.
    - Enhanced Fetch request handling to return empty results when caught up.
([cc2b4bd](https://github.com/darioajr/horizon/commit/cc2b4bdf3c58dcb09c8439ef1a806fecb96fa04f))

---
*Generated by [git-cliff](https://git-cliff.org)*
