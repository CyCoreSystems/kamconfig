# kamconfig - Kamailio Configuration Manager

Kam Config generates configuration templates for Kamailio.  In general, it does so
by producing a `kamconfig-vars.k` file which contains a set of `#!substdef` mappings of
variable names to dynamic runtime values.  Additionally, it will restart
kamailio when any of these values change or when the basic ConfigMap of the
kamailio configuration template changes.

When running kamailio, it should be told to look for its configuration file as:

`/config/kamailio/kamconfig-wrapper.k`

## Variable substitution

Variable substitutions are defined by
[kubetemplate](github.com/CyCoreSystems/kubetemplate), and they can reference
any value supported by that engine.

Any file name in a profile or a custom config set which ends in `.tmpl` will be
processed for variable substitution before being put in place, and its `.tmpl`
extension will be truncated.

Thus, a file named `vars.k.tmpl` with the following contents:

```
#!substdef "/PUBLICIP/{{.Network "publicv4"}}/"
#!substdef "/DB_IP/{{.ServiceIP "mysql" "db"}}/"
#!substdef "/DB_USERNAME/{{.Secret "mysql" "" "username"}}/"
#!substdef "/DB_PASSWORD/{{.Secret "mysql" "" "password"}}/"
```

Would become the file `vars.k` with contents similar to:

```
#!substdef "/PUBLICIP/172.16.2.23/"
#!substdef "/DB_IP/10.3.0.12/"
#!substdef "/DB_USERNAME/dbuser/"
#!substdef "/DB_PASSWORD/Sup3rS3cretP@ssw0rd/"
```

## Restarts

If any defined variable changes, kamailio will be restarted.  Thus, it is
generally a good idea to keep this list of variables as short as possible, to
prevent frequent restarts.  This can be done using indirection, such as DNS
names (which stay the same even as endpoints change).

Restarts are initiated by sending the `core.kill` command via RPC.  To
facilitate this, Kam Config starts a UDP RPC listener on port 9998.

## Template layers

Kam Config creates the final kamailio configuration by applying three layers, in
order.  Any subsequent layer which has the same filename as a previous layer
will replace the file from the previous layer, making it easy to completely
customize any aspect of the configuration.

The three layers are:
  - Core 
  - Profile 
  - Custom 

### Core template

In order for the restart to work, kamailio needs to have a certain core
configuration.  This is done by creating a wrapper configuration which includes
the required basic template and then imports `kamailio.cfg` from the external
configuration template.  Thus, the external configuration template should
_never_ use two file names:

**Never** use these file names:
 - `kamconfig-wrapper.k`
 - `kamconfig-modpath.k`
 - `kamconfig-vars.k`

### Profile

Profiles may be used to layer common use profile configurations into
the mix.  If a profile is specified, it will be added to the template layers
after the basic template.  Any custom (external) template will be applied after
the profile template, overwriting any files contained therein.

In general, profile templates contain insertion points for each route to allow
for customization.  Each route should look for files named
`route.d/<routename>_pre.k` and `route.d/<routename>_post.k`, and will include
them before and after the route's profile contents.

The file and directory structure of a profile is, in general, common.  It is
recommended but not required that custom configuration overlays maintain the
same structure.

All profile files end in the `.k` or `.k.tmpl` extensions, depending on whether
they have variable substitutions

Profiles may be supplied in one of two ways:
  - mount a .zip file named `profile.zip` to `/source/` inside the container
  - set the `PROFILE` environment variable to point to a .zip file
    containing the profile templates, either in the container filesystem or as
    an HTTP URL.

The entry-point of a profile is `profile.k`, and it should be stored in the root
directory of the profile.

#### Structure

There are a few basic requirements for a profile structure.  The first is the
entry-point of the template, which should always be named `profile.k` and be
located in the root of the profile tree structure.

Next, there should generally be, at a minimum, two subdirectories:

  - `module.d/` - module configurations
  - `route.d/`  - route scripts

In each of these directories, there should be one file per entity.  That is,
there is one file in `module.d/` for each module configuration, and one file in
`route.d/` for each named route.

In order to allow for easy customization, each route should contain an
`import_file` statement at the beginning and end, with the imported filename
being the route name plus `_pre.k` and `_post.k`, respectively.

For instance, for a route named `TRUSTED_CHECK`, there should be a file
`route.d/trusted_check.k`, which contains something like:

```kamailio
route[TRUSTED_CHECK] {
#!import_file "trusted_check_pre.k"
   
   if(!allow_trusted()) {
      exit;
   }

#!import_file "trusted_check_post.k"
}
```

### Custom

The custom configuration is that supplied by the end user.  It describes any
tweaks, special routines, site-specific configurations, etc, beyond what the
profile describes.  While it is recommended that the form match that of a
profile, the only requirement of the custom configuration is that its entry
point is:

`custom.k`

As always, if there are variable substitutions in this file, it will be
`custom.k.tmpl` in the template, which will be translated to `custom.k` at
runtime by `kamconfig`.

Like the profile, the custom configuration may be sourced by either:
  - mounting a zip file containing the configuration template tree to
    `/source/custom.zip` inside the container
  - setting the `SOURCE` environment variable to point to the location of the
    custom zip file, by local path or by HTTP URL.

## Dispatchers

One of the most dynamic components of most kamailio configurations is the
addresses comprising each of the dispatcher sets.  The specialized external
[dispatchers](github.com/CyCoreSystems/dispatchers) sidecar may be used to avoid
restarting kamailio every time the set of dispatchers changes.  Instead, a
`dispatcher.reload` will be called from the RPC service.

## Route check (TBD)

Kam Config also creates a special route by which it can verify proper
functioning of the kamailio instance.  This route is safe, in that it does not
allow any access to the system, and is triggered by the presence of the header
`X-KamailioConfig-RouteTest`.

