# traefik-throttler
[![release](https://img.shields.io/github/v/tag/clambin/traefik-throttler?color=green&label=release&style=plastic)](https://github.com/clambin/throttler/releases)
![test](https://github.com/clambin/traefik-throttler/workflows/test/badge.svg)
[![codecov](https://img.shields.io/codecov/c/gh/clambin/traefik-throttler?style=plastic)](https://app.codecov.io/gh/clambin/throttler)
[![license](https://img.shields.io/github/license/clambin/tado-exporter?style=plastic)](LICENSE.md)

This Traefik plugin throttles clients that generate too many 404 errors.

## Configuration

### Static

Add the plugin to your Traefik static configuration:

```yaml
experimental:
  plugins:
    throttler:
      moduleName: "github.com/clambin/traefik-throttler"
      version: "v0.2.3"
```

### Dynamic

Create a [middleware](https://docs.traefik.io/middlewares/overview/) in your dynamic configuration and
add it to your router(s).

The following example creates and uses the plugin to block requests after ten 404 errors, 
with an allowed error rate of two request per second.

```yaml
http:
  middlewares:
    throttler-foo:
      throttler:
        # Throttler uses a token bucket to throttle clients that generate too many 404 errors.
        # capacity is the number of 404 errors to allow before throttling (default: 50)
        capacity: 10
        # rate allows 404 errors while throttling (default: 1 req/sec)
        rate: 2
        log:
          # logging format (json/text; default: text)
          format: json
          # logging level (debug/info/warn/error; default: info)
          level: debug
  routers:
    my-router:
      rule: "Host(`example.com`)"
      service: my-service
      middlewares:
        - throttler-foo
  services:
    my-service:
      loadBalancer:
        servers:
          - url: "http://localhost:8080"
```

## Authors

* **Christophe Lambin**

## License

This project is licensed under the [MIT](LICENSE.md) license.
