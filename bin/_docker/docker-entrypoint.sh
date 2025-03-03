#!/bin/sh

epoch_time=$(date +%s)
infisical_version=$(infisical --version)

if [ -z "$INFISICAL_CLIENT_ID" ] || [ -z "$INFISICAL_CLIENT_SECRET" ]; then
  echo "{\"level\":\"warn\",\"time\":${epoch_time},\"message\":\"Infisical client authentication is not configured.\"}"
else
  export INFISICAL_DISABLE_UPDATE_CHECK=true
  export INFISICAL_TOKEN=$(infisical login --method=universal-auth --client-id=$INFISICAL_CLIENT_ID --client-secret=$INFISICAL_CLIENT_SECRET --silent --plain)
  # Log Infisical client configuration
  echo "{\"level\":\"info\",\"time\":${epoch_time},\"message\":\"Using ${infisical_version}\"}"
  echo "{\"level\":\"info\",\"time\":${epoch_time},\"message\":\"Infisical environment: ${INFISICAL_ENVIRONMENT}\"}"
  echo "{\"level\":\"info\",\"time\":${epoch_time},\"message\":\"Infisical project_id: ${INFISICAL_PROJECT_ID}\"}"

  infisical export --env=${INFISICAL_ENVIRONMENT} --projectId=${INFISICAL_PROJECT_ID} > /tmp/.env
  set -a
  source /tmp/.env
  set +a
  rm /tmp/.env

  unset INFISICAL_CLIENT_ID
  unset INFISICAL_CLIENT_SECRET
  unset INFISICAL_ENVIRONMENT
  unset INFISICAL_PROJECT_ID
  unset INFISICAL_TOKEN
fi

# Execute the application passing CMD arguments
exec "$@"
