# Pre-warmed Ollama image: bakes the summarization model into the image at build
# time so the container starts ready (no first-request pull). Override the model
# with --build-arg MODEL=... (and rebuild) to change it.
FROM ollama/ollama:latest

ARG MODEL=qwen2.5:0.5b
ENV PREPULL_MODEL=${MODEL}

# Start the server, pull the model, then stop. The layer keeps the model blob.
RUN ollama serve & \
    server_pid=$! && \
    for i in $(seq 1 30); do \
      ollama list >/dev/null 2>&1 && break; \
      sleep 1; \
    done && \
    ollama pull "${PREPULL_MODEL}" && \
    kill "${server_pid}"

# The base image already exposes 11434 and sets the entrypoint to `ollama serve`.
