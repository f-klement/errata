#!/bin/sh
# Pushes the image the pipeline built, and signs it.
docker push "registry.example.com/larder/api:$IMAGE_TAG"
cosign sign "registry.example.com/larder/api:$IMAGE_TAG"
