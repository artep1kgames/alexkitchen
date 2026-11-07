FROM postgres:17-alpine

COPY comments-app/schema.sql /docker-entrypoint-initdb.d/001-schema.sql

RUN chmod 644 /docker-entrypoint-initdb.d/001-schema.sql
