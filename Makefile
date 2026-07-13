APP_NAME := redoproxy
HOST := $(shell grep '^HOST=' .env 2>/dev/null | cut -d '=' -f 2)
SSH_USER ?= root
REMOTE_DIR ?= /opt/$(APP_NAME)
IMAGE ?= ghcr.io/mikhail-angelov/$(APP_NAME)
IMAGE_TAG=latest

.PHONY: check-host
check-host:
	@test -n "$(HOST)" || (echo "HOST is required. Set HOST in .env or pass HOST=example.com"; exit 1)

.PHONY: check-deploy-vars
check-deploy-vars: check-host
	@test -n "$(IMAGE_TAG)" || (echo "IMAGE_TAG is required. Pass IMAGE_TAG=v1.2.3 or sha-..."; exit 1)

.PHONY: install
install: check-host ## Copy .env and docker-compose.yml to remote host
	@echo "Installing $(APP_NAME) on $(HOST)..."
	ssh $(SSH_USER)@$(HOST) "mkdir -p $(REMOTE_DIR)"
	scp ./.env $(SSH_USER)@$(HOST):$(REMOTE_DIR)/.env
	scp ./docker-compose-st.yml $(SSH_USER)@$(HOST):$(REMOTE_DIR)/docker-compose-st.yml
	scp ./docker-compose.yml $(SSH_USER)@$(HOST):$(REMOTE_DIR)/docker-compose.yml
	@echo "Install complete. Run 'make deploy' to start."

.PHONY: deploy
deploy: check-deploy-vars ## Pull pinned Docker image and restart service on remote host
	@echo "Deploying $(APP_NAME) to $(HOST)..."
	ssh $(SSH_USER)@$(HOST) "docker pull $(IMAGE):$(IMAGE_TAG)"
	ssh $(SSH_USER)@$(HOST) "cd $(REMOTE_DIR) && REDOPROXY_IMAGE_TAG=$(IMAGE_TAG) docker compose up -d"
	@echo "Deploy complete."
