variable "IMAGE" {
  default = "ghcr.io/zhming0/fly-tunnel-operator"
}

variable "VERSION" {
  default = "latest"
}

group "default" {
  targets = ["production"]
}

target "dev" {
  dockerfile = "Dockerfile"
  target     = "dev"
}

target "production" {
  dockerfile = "Dockerfile"
  tags = [
    "${IMAGE}:${VERSION}",
    "${IMAGE}:latest"
  ]
  platforms = ["linux/amd64", "linux/arm64"]
}
