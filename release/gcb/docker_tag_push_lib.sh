#!/bin/bash
# Copyright 2018 Istio Authors. All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
################################################################################

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  echo "error this script should be sourced"
  exit 4
fi

if [[ -z "$(command -v docker)" ]]; then
    echo "Could not find 'docker' in path"
    exit 1
fi

# Add extra artifacts into each Docker image's tarball in OUT_PATH. The extra
# artifacts are specified in the 5th argument as a space-delimited list.
function add_extra_artifacts_to_tar_images() {
  local HUB
  HUB="$1"
  local TAG
  TAG="$2"
  local OUT_PATH
  OUT_PATH="$3"
  REPO="$4"
  read -r -a extra_artifacts <<< "$5"
  local add_cmd=""
  local tmpdir
  tmpdir=$(mktemp -d)

  pushd "${tmpdir}" || return 1
  for extra_artifact in "${extra_artifacts[@]}"; do
    # Copy artifact into current directory to bring it into the context of the
    # Docker daemon when we run 'dockre build' below.
    cp -r "${extra_artifact}" .
    add_cmd="${add_cmd}COPY $(basename "${extra_artifact}") /"$'\n'
  done

  if [[ -z "${add_cmd}" ]]; then
    echo >&2 "there was nothing to inject into the tar image"
    return 1
  fi

  for TAR_PATH in "${OUT_PATH}"/docker/*.tar.gz; do
    # if no docker/ directory or directory has no tar files
    if [[ "${TAR_PATH}" == "${OUT_PATH}/docker/*.tar.gz" ]]; then
      break
    fi
    set_image_vars "$TAR_PATH"

    docker load -i "${TAR_PATH}"

    cat >Dockerfile <<EOF
FROM ${REPO}/${IMAGE_NAME}:${TAG}${VARIANT_NAME}
${add_cmd}
EOF

    docker build -t              "${HUB}/${IMAGE_NAME}:${TAG}${VARIANT_NAME}" .
    # Include the license text in the tarball as well (overwrite old $TAR_PATH).
    docker save -o "${TAR_PATH}" "${HUB}/${IMAGE_NAME}:${TAG}${VARIANT_NAME}"
  done
  popd || return 1
}

function docker_tag_images() {
  local DST_HUB
  DST_HUB="$1"
  local DST_TAG
  DST_TAG="$2"
  local OUT_PATH
  OUT_PATH="$3"

  for TAR_PATH in "${OUT_PATH}"/docker/*.tar.gz; do
    # if no docker/ directory or directory has no tar files
    if [[ "${TAR_PATH}" == "${OUT_PATH}/docker/*.tar.gz" ]]; then
      break
    fi
    set_image_vars "$TAR_PATH"

    docker load -i "${TAR_PATH}"
    DOCKER_OUT=$(docker load -i "${TAR_PATH}")
    SRC_HUB=$(echo "$DOCKER_OUT" | cut -f 2 -d : | xargs dirname)
    SRC_TAG_WITH_VARIANT=$(echo "$DOCKER_OUT" | cut -f 3 -d :)


    docker tag "${SRC_HUB}/${IMAGE_NAME}:${SRC_TAG_WITH_VARIANT}" \
                "${DST_HUB}/${IMAGE_NAME}:${DST_TAG}${VARIANT_NAME}"
  done
}

function add_docker_creds() {
  local PUSH_HUB
  PUSH_HUB="$1"

  cp -r "${DOCKER_CONFIG}" "$HOME/.docker"
  export DOCKER_CONFIG="$HOME/.docker"
  if [[ "${PUSH_HUB}" == gcr.io* ]]; then
    gcloud auth configure-docker -q
  elif [[ "${PUSH_HUB}" == "docker.io/testistio" ]]; then
    gsutil -q cp "gs://istio-secrets/docker.test.json" "$HOME/.docker/config.json"
  fi
}

function docker_push_images() {
  local DST_HUB
  DST_HUB="$1"
  local DST_TAG
  DST_TAG="$2"
  local OUT_PATH
  OUT_PATH="$3"
  echo "pushing to ${DST_HUB}/image:${DST_TAG}"

  if [ -z "${LOCAL_BUILD+x}" ]; then
    add_docker_creds "${DST_HUB}"
  fi

  for TAR_PATH in "${OUT_PATH}"/docker/*.tar.gz; do
    # if no docker/ directory or directory has no tar files
    if [[ "${TAR_PATH}" == "${OUT_PATH}/docker/*.tar.gz" ]]; then
      break
    fi
    set_image_vars "$TAR_PATH"

    docker load -i "${TAR_PATH}"

    docker push "${DST_HUB}/${IMAGE_NAME}:${DST_TAG}${VARIANT_NAME}"
  done
}

function set_image_vars() {
  local TAR_PATH=$1
  BASE_NAME=$(basename "$TAR_PATH")
  TAR_NAME="${BASE_NAME%.*}"
  IMAGE_NAME="${TAR_NAME%.*}"
  VARIANT_NAME=""
  #check if it is a build variant (e.g. sidecar_injector-distroless)
  case "${IMAGE_NAME}" in
    *-distroless)
      # in case of a distroless tar file, we remove the "-distroless" from the image name
      VARIANT_NAME="-distroless"
      IMAGE_NAME="${IMAGE_NAME%${VARIANT_NAME}}"
      ;;
  esac
}
