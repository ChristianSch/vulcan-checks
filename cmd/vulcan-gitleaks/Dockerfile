# Copyright 2019 Adevinta

FROM zricethezav/gitleaks:v8.28.0

# Override base label with 🔑 not supported by artifactory.
LABEL org.opencontainers.image.description="Protect and discover secrets using Gitleaks"

# Override entrypoint
ENTRYPOINT ["/usr/bin/env"]


# Install check
ARG TARGETOS
ARG TARGETARCH
COPY ${TARGETOS}/${TARGETARCH}/vulcan-gitleaks /

CMD ["/vulcan-gitleaks"]
