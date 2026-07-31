<dl>
    <dt>Hosting status</dt>
    <dd>{$controllerStatus.state|escape}</dd>
    {if $controllerStatus.version}
        <dt>Version</dt>
        <dd>{$controllerStatus.version|escape}</dd>
    {/if}
</dl>

