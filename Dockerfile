# Copyright 2015 The Kubernetes Authors. All rights reserved.
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

FROM quay.io/aledbf/nginx-slim:0.10

RUN DEBIAN_FRONTEND=noninteractive apt-get update && apt-get install -y \
  diffutils \
  ssl-cert \
  --no-install-recommends \
  && rm -rf /var/lib/apt/lists/* \
  && make-ssl-cert generate-default-snakeoil --force-overwrite

COPY nginx-ingress-controller /
COPY nginx.tmpl /etc/nginx/template/nginx.tmpl
COPY nginx.tmpl /etc/nginx/nginx.tmpl
COPY default.conf /etc/nginx/nginx.conf

COPY lua /etc/nginx/lua/

WORKDIR /

CMD ["/nginx-ingress-controller"]