# configmap-updater

`configmap-updater`는 원격(대상) 클러스터의 소스 ConfigMap을 비교해 로컬 클러스터의 타겟 ConfigMap으로 동기화하는 Kubernetes 오퍼레이터입니다.

## CRDs

- `DestinationCluster`
  - `spec.kubeconfigSecretRef`(`Secret` key)로 원격 kubeconfig를 참조
  - `spec.pollInterval`로 기본 폴링 주기 정의
- `ConfigMapUpdater`
  - `DestinationCluster`를 참조
  - source/target ConfigMap 식별 정보 정의
  - ConfigMap 변경 시 대상 Deployment 재시작(선택)

## Safety

- 리컨사일 로직에서 원격 클러스터는 읽기 전용으로만 접근합니다.
- 원격 kubeconfig는 반드시 Kubernetes Secret 참조로 주입해야 합니다.
- kubeconfig/cert/key 원문을 CR spec에 직접 저장하지 않습니다.
- 컨트롤러는 원격 클러스터 리소스를 생성/수정/삭제하지 않습니다.

## Quick Start

```bash
make test
kubectl apply -k config/samples
```

샘플 적용 전 `config/samples/bf-destination-kubeconfig-secret.yaml`의 placeholder 값을 실제 kubeconfig 내용으로 교체하세요.

## 배경

애플리케이션 개발팀은 인프라를 독립적으로 관리할 수 없었고, 운영 설정 레포지토리에 대한 권한도 제한적이어서 전체 CI/CD를 직접 수행하기 어려웠습니다.

배포 병목을 줄이기 위해 기존에 ImageUpdater와 유사한 방식으로 이미지 동기화 기반 클론 환경은 운영하고 있었습니다.  
하지만 실제 애플리케이션 동작은 ConfigMap(특히 env 성격의 ConfigMap)에 크게 의존하므로, 이미지 동기화만으로는 충분하지 않았습니다.

운영 환경에서는 별도 형상관리/파이프라인으로 ConfigMap을 관리하지만, 개발팀은 해당 소스 레포 쓰기 권한이 없습니다.  
`configmap-updater`는 원격은 읽기 전용으로 유지하면서 운영과 유사한 ConfigMap 상태를 클론 클러스터에 리컨사일링하여 이 간극을 해소합니다.

## 목적

- 클론/개발 환경의 설정을 실제 환경 ConfigMap과 최대한 일치시킵니다.
- ConfigMap 의존도가 높은 애플리케이션의 설정 드리프트를 줄입니다.
- 운영 설정 레포 직접 권한 없이도 빠르고 안전한 검증을 가능하게 합니다.
- ImageUpdater와 유사한 운영 모델을 ConfigMap 동기화에 적용합니다.

## 아키텍처

오퍼레이터는 2개의 CRD를 중심으로 동작합니다.

1. `DestinationCluster`
- 소스 ConfigMap을 읽어올 원격 클러스터를 정의합니다.
- `spec.kubeconfigSecretRef`로 원격 접근 자격 정보를 참조합니다.
- 주기적 리컨사일을 위한 기본 폴링 정책을 정의합니다.

2. `ConfigMapUpdater`
- 원격 클러스터의 소스 ConfigMap 식별자를 정의합니다.
- 로컬 클러스터의 타겟 ConfigMap 식별자를 정의합니다.
- 하나의 `DestinationCluster`를 참조합니다.
- ConfigMap 변경 시 재시작할 Deployment 대상을 선택적으로 정의합니다.

## 리컨사일 흐름

1. `ConfigMapUpdater`를 조회합니다.
2. 참조된 `DestinationCluster`를 해석합니다.
3. Secret에서 kubeconfig를 읽어 원격 클라이언트를 구성합니다.
4. 원격 소스 ConfigMap을 읽기 전용으로 조회합니다.
5. source/target 내용을 비교합니다(hash/data/binaryData).
6. 변경이 있으면 로컬 target ConfigMap을 업데이트합니다.
7. `restartOnChange`가 활성화되어 있으면 지정된 Deployment를 patch해 롤링 재시작을 유도합니다.
8. status/conditions를 갱신하고 주기에 맞춰 재큐잉합니다.

## 사용 기술

- Go
- Kubebuilder
- controller-runtime/client-go
- Kubernetes CRD + Reconciler 패턴
- Secret 참조 기반 원격 kubeconfig 로딩
- ConfigMap 내용 hash 기반 변경 감지
- Deployment annotation patch 기반 재시작 트리거
