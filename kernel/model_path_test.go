package kernel

import (
	"strconv"
	"strings"
	"testing"
)

func qwen36Shape() ModelShape {
	t := func(n string) ModelTensor { return ModelTensor{Name: n, Dtype: "BF16", Shape: []int64{8}} }
	var ts []ModelTensor
	for i := 0; i < 40; i++ {
		l := "model.language_model.layers." + itoaT(i) + "."
		ts = append(ts, t(l+"input_layernorm.weight"), t(l+"post_attention_layernorm.weight"), t(l+"mlp.gate.weight"),
			t(l+"mlp.shared_expert.gate_proj.weight"), t(l+"mlp.shared_expert_gate.weight"), t(l+"self_attn.k_proj.weight"),
			t(l+"linear_attn.in_proj_qkvz.weight"), t(l+"linear_attn.A_log"))
	}
	for i := 0; i < 27; i++ {
		b := "model.visual.blocks." + itoaT(i) + "."
		ts = append(ts, t(b+"norm1.weight"), t(b+"attn.qkv.weight"), t(b+"mlp.linear_fc1.weight"))
	}
	ts = append(ts, t("model.visual.patch_embed.proj.weight"), t("model.visual.pos_embed.weight"), t("model.visual.merger.norm.weight"),
		t("mtp.layers.0.input_layernorm.weight"), t("mtp.layers.0.post_attention_layernorm.weight"), t("mtp.layers.0.mlp.gate.weight"),
		t("mtp.layers.0.mlp.shared_expert.gate_proj.weight"), t("mtp.layers.0.self_attn.k_proj.weight"), t("mtp.fc.weight"), t("mtp.pre_fc_norm_embedding.weight"),
		t("mtp.norm.weight"), t("model.language_model.embed_tokens.weight"), t("model.language_model.norm.weight"), t("lm_head.weight"))
	return ModelShape{Tensors: ts, Config: map[string]any{
		"architectures": []any{"Qwen3_5MoeForConditionalGeneration"},
		"text_config":   map[string]any{"num_hidden_layers": float64(40), "mtp_num_hidden_layers": float64(1)},
		"vision_config": map[string]any{"depth": float64(27)},
	}}
}

func itoaT(i int) string { return strconv.Itoa(i) }

// 11/23 16:58:59 and 17:12:02 the parts visual and mtp were started beside the ones the tokens
// need: config.json's num_hidden_layers (40) counts model.language_model's layers, the tensors
// under model.visual's 27 blocks and mtp's 1 layer are off that path, and a part naming only
// those is not started
func TestPartsNamingOnlyTensorsOffTheTextPathAreNotStarted(t *testing.T) {
	ms := qwen36Shape()
	p := measureModelPath(ms)
	if p.TextRoot != "model.language_model" || strings.Join(p.OffRoots, " ") != "model.visual mtp" {
		t.Fatalf("%+v", p)
	}
	if !p.Off("mtp.fc.weight") || !p.Off("model.visual.patch_embed.proj.weight") || p.Off("lm_head.weight") || p.Off("model.language_model.norm.weight") {
		t.Fatal("the path's groups")
	}
	parts := []string{
		"loader: reads the 8 named tensor groups from the .safetensors files",
		"embed: gathers BF16 embedding rows by token ID from embed_tokens.weight",
		"layer_norm: applies a weighted RMS normalization using one input_layernorm or post_attention_layernorm weight vector",
		"moe: multiplies the hidden state by mlp.gate.weight, runs the shared expert (gate_proj ...), gates it with shared_expert_gate",
		"visual: runs the full ViT vision encoder: projects image patches via patch_embed.proj (3-D conv), adds pos_embed, passes through 27 transformer blocks (norm1 → QKV → proj → norm2 → MLP), and applies the two-layer merger MLP (norm → linear_fc1 → GELU → linear_fc2)",
		"mtp: runs the MTP drafter: applies pre_fc_norm_embedding and pre_fc_norm_hidden to the respective inputs, concatenates and projects through fc.weight to form the MTP hidden state, then runs one transformer layer (input_layernorm → QSA sub-layer → post_attention_layernorm → MoE FFN) using the mtp.layers.{i}.* weights, and applies mtp.norm at the end",
	}
	keep, off := offPathParts(ms, parts)
	var kept []string
	for _, k := range keep {
		n, _, _ := strings.Cut(k, ":")
		kept = append(kept, n)
	}
	if strings.Join(kept, " ") != "loader embed layer_norm moe" || len(off) != 2 || !strings.HasPrefix(off[0], "visual (names ") || !strings.HasPrefix(off[1], "mtp (names ") {
		t.Fatalf("kept %v off %v", kept, off)
	}
	// a model whose config counts no layered root has no path measured: every part stays
	ms.Config = map[string]any{}
	if keep, off := offPathParts(ms, parts); len(keep) != len(parts) || off != nil {
		t.Fatalf("%v %v", keep, off)
	}
}
