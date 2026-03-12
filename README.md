# Spoxy - Spotify Metadata Resolver

Spoxy is a high-performance, resilient Spotify metadata resolver that bypasses the limitations of the official Spotify Web API by using the Partner (GraphQL) API. It supports extracting metadata for tracks, albums, and playlists.

## Features

-   **Deep Integration**: Resolves tracks, albums, and playlists.
-   **Dynamic Secrets**: Automatically extracts TOTP secrets and GraphQL hashes from Spotify's client bundles, making it resistant to Spotify's internal changes.
-   **Official Schema**: Returns responses that closely follow the official Spotify API format for easy integration.
-   **High Performance**: Uses Redis to cache metadata and server-side tokens.
-   **Proxy Support**: Supports optional HTTP/HTTPS proxies for all outgoing requests.
-   **Resilient Authentication**: Implements `clienttoken` retrieval and dynamic TOTP generation.

## Prerequisites

-   [Podman](https://podman.io/) or [Docker](https://www.docker.com/)
-   [Podman Compose](https://github.com/containers/podman-compose) or [Docker Compose](https://docs.docker.com/compose/)

## Getting Started

### Quick Start with Docker/Podman

1.  Clone the repository:
    ```bash
    git clone https://github.com/DesDoIt/spoxy.git
    cd spoxy
    ```

2.  Start the service:
    ```bash
    podman-compose up --build
    ```
    The API will be available at `http://localhost:8080`.

### Environment Variables

| Variable | Description | Default |
| :--- | :--- | :--- |
| `PORT` | Port the API server listens on | `8080` |
| `SPOXY_REDIS_URL` | Redis connection string | `redis://redis:6379/0` |
| `SPOXY_PROXY_URL` | Optional proxy URL (e.g., `http://user:pass@host:port`) | `""` |

## API Documentation

### Resolve Metadata

Resolves a Spotify link into its metadata.

-   **Endpoint**: `GET /api/resolve`
-   **Query Parameters**:
    -   `link` (required): A valid Spotify track, album, or playlist URL.
-   **Sample Request**:
    ```bash
    curl "http://localhost:8080/api/resolve?link=https://open.spotify.com/track/4iV5W9uYEdYUVa79Axb7Rh"
    ```
-   **Sample Response**:
    ```json
    [
      {
        "album": {
          "name": "Abramyan: Preludes...",
          "release_date": "2010-01-01",
          "images": [...]
        },
        "artists": [
          {
            "name": "Eduard Abramyan",
            "id": "5EuJ3aXZp3JKHsk7g32Vbd"
          }
        ],
        "name": "Prelude for Piano No. 11 in F-Sharp Minor",
        "duration_ms": 248293,
        "id": "4iV5W9uYEdYUVa79Axb7Rh"
      }
    ]
    ```

### Error Codes

-   `200 OK`: Success.
-   `404 Not Found`: No tracks found for the given link.
-   `400 Bad Request`: Unsupported link type.
-   `500 Internal Server Error`: Unexpected error resolving metadata.

## Development & Debugging

### Running in Debug Mode

To run with Delve debugger attached:

```bash
podman-compose -f docker-compose.debug.yml up --build
```

You can then attach your debugger (e.g., via VS Code) to `localhost:4000`.

### VS Code Debugging

A `launch.json` is provided in the `.vscode` directory with two configurations:
1.  **SPOXY Docker Debug**: Attaches to the debugger running inside the container.
2.  **SPOXY Local Debug**: Runs the application locally (requires Redis to be available).

## License

MIT
