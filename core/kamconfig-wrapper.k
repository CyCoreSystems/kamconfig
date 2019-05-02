#!KAMAILIO

#!include_file "kamconfig-vars.k"
#!include_file "kamconfig-modpath.k"

loadmodule "ctl"
modparam("ctl", "binrpc", "udp:127.0.0.1:9998")

#!import_file "profile.k"
#!import_file "custom.k"
