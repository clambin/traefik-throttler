# traefik-throttler
[![release](https://img.shields.io/github/v/tag/clambin/traefik-throttler?color=green&label=release&style=plastic)](https://github.com/clambin/throttler/releases)
![test](https://github.com/clambin/traefik-throttler/workflows/test/badge.svg)
[![codecov](https://img.shields.io/codecov/c/gh/clambin/traefik-throttler?style=plastic)](https://app.codecov.io/gh/clambin/throttler)
[![license](https://img.shields.io/github/license/clambin/tado-exporter?style=plastic)](LICENSE.md)

traefik-throttler throttles clients that generate too many 404 errors.

## Installation

Add the plugin to your Traefik static configuration:

```yaml
experimental:
  plugins:
    throttler:
      moduleName: "github.com/clambin/traefik-throttler"
      version: "v0.2.0"
```

## Configuration

Create a middleware for the plugin. E.g. in kubernetes:

```yaml
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: throttler
spec:
  plugin:
    throttler:
      # Throttler uses a token bucket to throttle clients that generate too many 404 errors.
      # capacity is the number of 404 errors to allow before throttling (default: 50)
      capacity: 10
      # rate allows 404 errors while throttling (default: 1 req/sec)
      rate: 5
      log:
        # logging format (json/text; default: text)
        format: json
        # logging level (debug/info/warn/error; default: info)
        level: debug
```

Next, add the middleware to your router(s). E.g., in kubernetes:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: my-route
spec:
  parentRefs:
  - name: traefik-gateway
    sectionName: websecure
  hostnames:
  - www.example.com
  rules:
  - backendRefs:
    - name: my-service
      port: 80
    filters:
    - type: ExtensionRef
      extensionRef:
        group: traefik.io
        kind: Middleware
        name: throttler
```

## Authors

* **Christophe Lambin**

## License

This project is licensed under the MIT License. See the [LICENSE.md](LICENSE.md) file for details.
