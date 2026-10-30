import { createModels } from "@earendil-works/pi-ai/models";
import { opencodeGoProvider } from "@earendil-works/pi-ai/providers/opencode-go";

const models = createModels();
models.setProvider(opencodeGoProvider());
for (const model of models.getModels("opencode-go")) {
	console.log(`${model.id}\t${model.contextWindow ?? "?"}\t${model.name ?? ""}`);
}
