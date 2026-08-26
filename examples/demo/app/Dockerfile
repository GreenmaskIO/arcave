# Copyright 2026 Greenmask Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Build the demo app as a static binary, then run it on a minimal alpine image.
# alpine ships busybox wget, which the compose healthcheck uses to hit /healthz.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /arcave-demo-app .

FROM alpine:3.20
COPY --from=build /arcave-demo-app /usr/local/bin/arcave-demo-app
EXPOSE 8080
ENTRYPOINT ["arcave-demo-app"]
