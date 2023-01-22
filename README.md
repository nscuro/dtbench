# dtbench

Rudimentary load testing tool for [Dependency-Track](https://github.com/DependencyTrack/dependency-track).

## Usage

```shell
docker run -it --rm -v "/path/to/boms:/work:ro" ghcr.io/nscuro/dtbench:latest \
  -url http://host.docker.internal:8080 -pass admin123 \
  -boms /work -count 100 \
  -wait -wait-timeout 15m \
  -delay 1s
```
