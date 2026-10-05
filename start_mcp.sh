#!/bin/bash

export MCP_PUBLIC_URL='https://mcp-msk.v02.ru'
export GITHUB_CLIENT_ID='CLIENT_ID_ИЗ_GITHUB'
export GITHUB_CLIENT_SECRET='CLIENT_SECRET_ИЗ_GITHUB'

export MCP_CLIENT_ID='ghf-chatgpt'
export MCP_CLIENT_SECRET="$(openssl rand -hex 32)"
export MCP_ALLOWED_USERS='onnov'
export MCP_WRITE_REPOS='onnov/mcp'

./github_public_mcp
