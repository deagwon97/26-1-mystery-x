FROM eclipse-temurin:25-jdk AS builder
WORKDIR /app

ENV JAVA_HOME=/opt/java/openjdk

RUN apt-get update && apt-get install -y curl unzip vim jq sqlite3

COPY build.gradle.kts settings.gradle.kts gradlew ./
COPY gradle ./gradle
RUN chmod +x gradlew && ./gradlew dependencies --no-daemon

COPY ./ /app/src

WORKDIR /app/src

RUN ./gradlew clean bootJar -x test --no-daemon

FROM eclipse-temurin:25-jre
WORKDIR /app/src

COPY --from=builder /app/src/build/libs/*.jar app.jar

RUN mkdir -p /app/src/data

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s CMD curl -f http://localhost:8080/health || exit 1

EXPOSE 8080

ENTRYPOINT ["java","-jar","/app/src/app.jar"]