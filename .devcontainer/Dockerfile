FROM golang:1.27-bookworm

# Install Node.js 22.x
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
&& apt-get install -y nodejs && rm -rf /var/lib/apt/lists/*
