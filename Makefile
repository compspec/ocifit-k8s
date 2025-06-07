IMAGE_NAME ?= ghcr.io/compspec/ocifit-k8s
IMAGE_TAG ?= latest
FULL_IMAGE_NAME = $(IMAGE_NAME):$(IMAGE_TAG)
DOCKERFILE_PATH = Dockerfile
BUILD_CONTEXT = .

# Default target: builds the Docker image
all: build

# Build the Docker image
build:
	@echo "Building Docker image $(FULL_IMAGE_NAME)..."
	docker build \
		-f $(DOCKERFILE_PATH) \
		-t $(FULL_IMAGE_NAME) \
		$(BUILD_CONTEXT)
	@echo "Docker image $(FULL_IMAGE_NAME) built successfully."

# Push the docker image
push:
	@echo "Pushing image $(FULL_IMAGE_NAME)..."
	docker push $(FULL_IMAGE_NAME)

# Install the webhook
install:
	@echo "Installing $(FULL_IMAGE_NAME)..."
	kubectl apply -f ./deploy/webhook.yaml

# Install the webhook
uninstall:
	@echo "Uninstalling $(FULL_IMAGE_NAME)..."
	kubectl delete -f ./deploy/webhook.yaml

# Remove the image (clean with rmi)
clean:
	@echo "Removing Docker image $(FULL_IMAGE_NAME)..."
	docker rmi $(FULL_IMAGE_NAME) || true
	@echo "Docker image $(FULL_IMAGE_NAME) removed (if it existed)."

.PHONY: all build clean

