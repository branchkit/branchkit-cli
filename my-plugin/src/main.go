package main

import "github.com/branchkit/plugin-sdk-go"

type RenderSettingsRequest struct {
	TabKey string `json:"tab_key"`
}

func main() {
	plugin := branchkit.NewPlugin()

	plugin.HandleAction("myplugin.greet", func(req *branchkit.OnActionRequest) (any, error) {
		var p GreetParams
		req.UnmarshalParams(&p)

		name := "BranchKit"
		if p.Name != nil {
			name = *p.Name
		}

		plugin.Call("input.type_text", map[string]any{"text": "Hello, " + name + "!"}, nil)
		return nil, nil
	})

	branchkit.HandleTyped(plugin, "render_settings", func(_ *RenderSettingsRequest) (any, error) {
		return branchkit.RenderSettingsResponse{
			HTML: `<div style="padding: 16px; font-family: system-ui;">
	<h2 style="margin: 0 0 12px 0;">My Plugin</h2>
	<p style="color: #888; margin: 0 0 16px 0;">A BranchKit plugin</p>

	<h3 style="margin: 0 0 8px 0;">Voice Commands</h3>
	<table style="border-collapse: collapse; width: 100%;">
		<tr>
			<td style="padding: 6px 12px; border-bottom: 1px solid #333;"><em>"hello branchkit"</em></td>
			<td style="padding: 6px 12px; border-bottom: 1px solid #333; color: #888;">Types "Hello, BranchKit!"</td>
		</tr>
		<tr>
			<td style="padding: 6px 12px;"><em>"hello &lt;name&gt;"</em></td>
			<td style="padding: 6px 12px; color: #888;">Types "Hello, &lt;name&gt;!" with any spoken word</td>
		</tr>
	</table>
</div>`,
		}, nil
	})

	plugin.Run()
}
