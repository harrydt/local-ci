package workflow

import (
	"reflect"
	"testing"
)

const routed = "    runs-on: ${{ fromJSON(vars.LOCALCI_RUNNERS || '{}')[github.actor] || 'ubuntu-latest' }}\n"

func TestCheck(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []Finding
	}{
		{
			name: "routed job without secrets",
			src: "on: push\njobs:\n  test:\n" + routed +
				"    steps:\n      - run: go test ./...\n        env:\n          TOKEN: ${{ secrets.GITHUB_TOKEN }}\n",
		},
		{
			name: "routed job with a secret",
			src:  "jobs:\n  test:\n" + routed + "    steps:\n      - run: make\n        env:\n          KEY: ${{ secrets.DEPLOY_KEY }}\n",
			want: []Finding{{Job: "test", What: "secrets.DEPLOY_KEY"}},
		},
		{
			name: "hosted job with a secret is fine",
			src:  "jobs:\n  deploy:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo ${{ secrets.DEPLOY_KEY }}\n",
		},
		{
			name: "bracket, toJSON and repeat",
			src: "jobs:\n  t:\n" + routed +
				"    steps:\n      - run: echo ${{ secrets['A'] }} ${{ toJSON(secrets) }} ${{ secrets.B }} ${{ secrets.B }}\n",
			want: []Finding{{Job: "t", What: "secrets"}, {Job: "t", What: "secrets.B"}},
		},
		{
			name: "environment key and OIDC",
			src: "jobs:\n  deploy:\n" + routed +
				"    environment: production\n    permissions:\n      id-token: write\n    steps:\n      - run: ./deploy\n",
			want: []Finding{{Job: "deploy", What: "environment"}, {Job: "deploy", What: "id-token: write"}},
		},
		{
			name: "environment as an action input is not an environment",
			src:  "jobs:\n  t:\n" + routed + "    steps:\n      - uses: some/action@v1\n        with:\n          environment: dev\n",
		},
		{
			name: "workflow-level env and permissions apply to routed jobs only",
			src: "on: push\npermissions:\n  id-token: write\nenv:\n  K: ${{ secrets.K }}\njobs:\n  local:\n" + routed +
				"    steps:\n      - run: make\n  hosted:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make\n",
			want: []Finding{{Job: "local", What: "id-token: write"}, {Job: "local", What: "secrets.K"}},
		},
		{
			name: "commented-out secret and secrets: inherit are ignored",
			src:  "jobs:\n  t:\n" + routed + "    steps:\n      # - run: echo ${{ secrets.OLD }}\n      - run: make\n  call:\n    uses: ./.github/workflows/r.yml\n    secrets: inherit\n",
		},
		{
			name: "top-level key after jobs ends the jobs block",
			src:  "jobs:\n  t:\n" + routed + "    steps:\n      - run: make\nenv:\n  K: ${{ secrets.K }}\n",
			want: []Finding{{Job: "t", What: "secrets.K"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Check([]byte(tt.src)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Check() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
