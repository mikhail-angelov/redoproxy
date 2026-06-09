APP_NAME := redoproxy
HOST := $(shell grep '^HOST=' .env 2>/dev/null | cut -d '=' -f 2)

.PHONY: install
install: ## Copy .env and docker-compose.yml to remote host
	@echo "Installing $(APP_NAME) on $(HOST)..."
	-ssh root@$(HOST) "mkdir -p /opt/$(APP_NAME)"
	scp ./.env root@$(HOST):/opt/$(APP_NAME)/.env
	scp ./docker-compose-st.yml root@$(HOST):/opt/$(APP_NAME)/docker-compose-st.yml
	scp ./docker-compose.yml root@$(HOST):/opt/$(APP_NAME)/docker-compose.yml
	@echo "Install complete. Run 'make deploy' to start."

# Deploy — pull latest image and restart
.PHONY: deploy
deploy: ## Pull latest Docker image and restart service on remote host
	@echo "Deploying $(APP_NAME) to $(HOST)..."
	ssh root@$(HOST) "docker pull ghcr.io/mikhail-angelov/$(APP_NAME):latest"
	-ssh root@$(HOST) "cd /opt/$(APP_NAME) && docker compose down"
	ssh root@$(HOST) "cd /opt/$(APP_NAME) && docker compose up -d"
	@echo "Deploy complete."