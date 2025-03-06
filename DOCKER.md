# Docker Compose

This section will help guide you through the required steps to get started running the coredns-dnsimple in a Docker Compose environment.

## Prerequisites

- [Docker](https://docs.docker.com/install/)
- [Docker Compose](https://docs.docker.com/compose/install/)
- Logged in to the Docker Hub with access to the `dnsimple` organization – reach out to the platform team if you need access.

## Getting Started

### Start the Application

Run the following command to start the application:

```shell
docker compose up
```

### Stop the Application

To stop the application, simply type `CTRL+C` to stop the docker compose process.

### Clean up

You may selectively delete containers and volumes via the Docker Desktop application.

To remove all containers and associated volumes, run the following command:

```shell
docker compose down --remove-orphans --volumes
```

## Advanced Usage

### Selecting the App Version

You can select the version of the app to run by setting the `TAG` environment variable. The default is `latest`. If you want to run a specific version, set the `TAG` environment variable to the [tag](https://hub.docker.com/repository/docker/dnsimple/coredns-dnsimple/general) of the image you want to run.

Example:

```shell
TAG=v1.3.0 docker compose up
```

## Building a custom image

This document will help guide you through the required steps to get started building the app into Docker images. If you want more detailed information on the tools and configuration we will be using refer to the [CONTRIBUTING.md](CONTRIBUTING.md) document.

### Build the Image

```shell
make docker-build
```

### Build image with a specific tag

```shell
PACKAGER_VERSION=dev make docker-build
```
