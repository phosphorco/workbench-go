#!/bin/sh
set -eu

while IFS= read -r request; do
    id=$(printf '%s\n' "$request" | sed -n 's#.*"id":\([0-9][0-9]*\),"method".*#\1#p')
    if printf '%s\n' "$request" | grep -q '"method":"initialize"'; then
        printf '%s\n' "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"accepted\":true,\"capabilities\":[\"contribute\"],\"reasons\":[]}}"
    elif printf '%s\n' "$request" | grep -q '"method":"context.contribute"'; then
        printf '%s\n' "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"output\":{\"contributions\":[{\"slot\":{\"key\":\"example-slot\"},\"source\":{\"identity\":{\"id\":\"example-guidance\"},\"kind\":\"file\",\"path\":\"provider.md\"},\"body\":\"Review the project boundary before changing it.\",\"reasons\":[]}],\"reasons\":[]}}}"
    elif printf '%s\n' "$request" | grep -q '"method":"shutdown"'; then
        printf '%s\n' "{\"jsonrpc\":\"2.0\",\"id\":$id,\"result\":{\"requestId\":$id,\"reasons\":[]}}"
        exit 0
    fi
done
