FROM eclipse-temurin:25-jre-alpine

LABEL org.opencontainers.image.source="https://github.com/Hyeon9mak/26-1-mystery-x"
LABEL org.opencontainers.image.description="BuChoiPark Spring Boot Application"

# Install wget for healthcheck
RUN apk add --no-cache wget

# Create non-root user for security
RUN addgroup -S spring && adduser -S spring -G spring

WORKDIR /app

# Copy JAR file (relative to Docker context: BuChoiPark/)
COPY build/libs/BuChoiPark-0.0.1-SNAPSHOT.jar app.jar

RUN chown spring:spring app.jar

USER spring:spring

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=40s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8080/actuator/health || exit 1

ENTRYPOINT ["java", "-jar", "app.jar"]
CMD []
