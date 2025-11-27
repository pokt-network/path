#######################
### Proto  Helpers ####
#######################

.PHONY: proto_gen
proto_gen: proto_gen_observation ## Generate all protobuf artifacts

.PHONY: proto_regen
proto_regen: proto_clean proto_gen ## Regenerate all protobuf artifacts

.PHONY: proto_gen_observation
proto_gen_observation: ## Generate observation protobuf artifacts
	@echo "Generating observation protobuf artifacts..."
	@protoc -I=./proto \
		--go_out=./observation \
		--go_opt=module=github.com/pokt-network/path/observation \
		./proto/path/*.proto \
		./proto/path/metadata/*.proto \
		./proto/path/protocol/*.proto \
		./proto/path/qos/*.proto

.PHONY: proto_clean
proto_clean: ## Delete existing protobuf artifacts (i.e. .pb.go files)
	@echo "Deleting existing protobuf artifacts..."
	@find . -name "*.pb.go" -delete

.PHONY: proto_mock_gen
proto_mock_gen: ## Generate mocks for protobuf artifacts
	@echo "Generating mocks for protobuf artifacts..."
	@mockgen -source=./router/router.go -destination=./router/router_mock_test.go -package=router
